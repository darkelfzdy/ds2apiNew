package mihomo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"ds2api/internal/config"
)

// PoolResetter 抽象账号池重置动作，避免 mihomo 包反向依赖 account 包。
type PoolResetter interface {
	Reset()
}

// procHandle 跟踪一个已拉起的 mihomo 子进程。
type procHandle struct {
	cmd  *exec.Cmd
	done chan struct{} // Wait 返回后关闭
}

// Manager 负责 mihomo 子进程的生命周期与运行时配置热更新。
// 所有配置读写经由 config.Store 事务完成；Apply 重新生成 YAML 并重启进程。
type Manager struct {
	store *config.Store
	pool  PoolResetter

	mu          sync.Mutex
	handle      *procHandle
	running     bool
	pid         int
	startedAt   time.Time
	lastErr     string
	binary      string
	listenPorts []int
	applying    bool // 串行化 Apply，避免并发重启竞争

	dlMu sync.Mutex
	dl   downloadState // 内核下载任务状态（独立锁，避免阻塞主状态锁）
}

func NewManager(store *config.Store, pool PoolResetter) *Manager {
	return &Manager{store: store, pool: pool}
}

// Supported 报告当前部署形态能否拉起子进程（Vercel Serverless 不支持）。
func (m *Manager) Supported() bool {
	return !config.IsVercel()
}

// WorkDir 返回 mihomo 运行时目录（runtime.yaml / mihomo.log）。
func WorkDir() string {
	return config.ResolvePath("DS2API_MIHOMO_DIR", "data/mihomo")
}

// StartIfEnabled 在服务启动时调用：配置启用则后台拉起 mihomo。
func (m *Manager) StartIfEnabled() {
	if !m.Supported() || m == nil || m.store == nil {
		return
	}
	if !m.store.Snapshot().Mihomo.Enabled {
		return
	}
	go func() {
		if err := m.Apply(context.Background()); err != nil {
			config.Logger.Warn("[mihomo] startup apply failed", "error", err)
		}
	}()
}

// Apply 依据当前配置重建 runtime.yaml 并（重）启动 mihomo 进程。
// 配置未启用时确保进程停止。幂等，可被绑定/订阅/设置变更反复触发。
func (m *Manager) Apply(_ context.Context) error {
	if m == nil || m.store == nil {
		return errors.New("mihomo manager 未初始化")
	}
	if !m.Supported() {
		return errors.New("当前部署形态（Vercel）不支持 Mihomo 子进程")
	}
	m.mu.Lock()
	if m.applying {
		m.mu.Unlock()
		return errors.New("另一个应用操作正在进行中")
	}
	m.applying = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.applying = false
		m.mu.Unlock()
	}()

	cfg := m.store.Snapshot()
	if !cfg.Mihomo.Enabled {
		m.stopProcess()
		m.mu.Lock()
		m.running = false
		m.pid = 0
		m.listenPorts = nil
		m.lastErr = ""
		m.mu.Unlock()
		config.Logger.Info("[mihomo] bridge disabled, process stopped")
		return nil
	}

	yamlBytes, bindings, err := BuildRuntimeYAML(cfg)
	if err != nil {
		m.setLastErr(err.Error())
		return err
	}
	workDir := WorkDir()
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		m.setLastErr(err.Error())
		return fmt.Errorf("创建 mihomo 目录失败: %w", err)
	}
	configPath := filepath.Join(workDir, "runtime.yaml")
	if err := writeFileAtomic(configPath, yamlBytes); err != nil {
		m.setLastErr(err.Error())
		return fmt.Errorf("写入 mihomo 配置失败: %w", err)
	}

	binary, err := resolveBinary(cfg.Mihomo.BinaryPath)
	if err != nil {
		m.setLastErr(err.Error())
		return err
	}
	// 重新应用场景：先停掉旧进程释放其占用的端口，再做端口预检，
	// 否则我们自己在监听的 api_port/listener 端口会被误判为外部占用，
	// 导致任何重新应用都必然失败。
	m.stopProcess()
	if err := preflightPorts(cfg.Mihomo, bindings); err != nil {
		m.setLastErr(err.Error())
		return err
	}

	if err := m.startProcess(binary, workDir, configPath, cfg.Mihomo.APIPort); err != nil {
		m.setLastErr(err.Error())
		return err
	}

	ports := make([]int, 0, len(bindings))
	for _, b := range bindings {
		ports = append(ports, b.Port)
	}
	m.mu.Lock()
	m.running = true
	m.listenPorts = ports
	m.lastErr = ""
	m.binary = binary
	m.mu.Unlock()
	config.Logger.Info("[mihomo] bridge applied", "binary", binary, "listeners", len(bindings), "ports", ports)
	return nil
}

// Stop 终止 mihomo 子进程（服务退出时调用）。
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.stopProcess()
}

// Status 汇总运行状态供管理接口展示。
func (m *Manager) Status() map[string]any {
	cfg := m.store.Snapshot()
	m.mu.Lock()
	running, pid, lastErr, binary := m.running, m.pid, m.lastErr, m.binary
	startedAt, ports := m.startedAt, append([]int(nil), m.listenPorts...)
	m.mu.Unlock()

	type listenerView struct {
		Port     int      `json:"port"`
		Node     string   `json:"node"`
		NodeKey  string   `json:"node_key"`
		Accounts []string `json:"accounts"`
	}
	listeners := []listenerView{}
	if running {
		for _, port := range ports {
			if !portListening(port) {
				running = false // 进程看似活着但端口未监听，降级为异常态
				break
			}
		}
	}
	// 绑定视图：节点键 -> 账号列表
	accountsByProxy := map[string][]string{}
	for _, acc := range cfg.Accounts {
		pid := strings.TrimSpace(acc.ProxyID)
		if config.IsMihomoManagedProxyID(pid) {
			accountsByProxy[pid] = append(accountsByProxy[pid], acc.Identifier())
		}
	}
	for _, nodeKey := range cfg.Mihomo.SortedPortMapKeys() {
		proxyID := config.MihomoManagedProxyID(nodeKey)
		accs := accountsByProxy[proxyID]
		if len(accs) == 0 {
			continue
		}
		node, ok := cfg.Mihomo.FindMihomoNode(nodeKey)
		if !ok {
			continue
		}
		listeners = append(listeners, listenerView{
			Port:     cfg.Mihomo.PortMap[nodeKey],
			Node:     node.Name,
			NodeKey:  nodeKey,
			Accounts: accs,
		})
	}

	return map[string]any{
		"supported":     m.Supported(),
		"enabled":       cfg.Mihomo.Enabled,
		"running":       running,
		"pid":           pid,
		"binary":        binary,
		"binary_path":   cfg.Mihomo.BinaryPath,
		"binary_found":  detectBinary(cfg.Mihomo.BinaryPath),
		"download":      m.DownloadInfo(),
		"work_dir":      WorkDir(),
		"base_port":     cfg.Mihomo.BasePort,
		"api_port":      cfg.Mihomo.APIPort,
		"api_addr":      fmt.Sprintf("127.0.0.1:%d", cfg.Mihomo.APIPort),
		"started_at":    startedAt.Unix(),
		"last_error":    lastErr,
		"listeners":     listeners,
		"subscriptions": len(cfg.Mihomo.Subscriptions),
	}
}

// detectBinary 判断按给定配置能否解析到 mihomo 可执行文件。
func detectBinary(configured string) bool {
	_, err := resolveBinary(configured)
	return err == nil
}

func (m *Manager) setLastErr(msg string) {
	m.mu.Lock()
	m.lastErr = msg
	m.running = false
	m.mu.Unlock()
	config.Logger.Warn("[mihomo] apply failed", "error", msg)
}

// startProcess 拉起 mihomo 并等待其就绪（external-controller 可连接）。
func (m *Manager) startProcess(binary, workDir, configPath string, apiPort int) error {
	logPath := filepath.Join(workDir, "mihomo.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开 mihomo 日志失败: %w", err)
	}
	cmd := exec.Command(binary, "-d", workDir, "-f", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	hideSubprocessWindow(cmd)
	if err := cmd.Start(); err != nil {
		if closeErr := logFile.Close(); closeErr != nil {
			config.Logger.Warn("[mihomo] close log file failed", "error", closeErr)
		}
		return fmt.Errorf("启动 mihomo 失败: %w", err)
	}
	handle := &procHandle{cmd: cmd, done: make(chan struct{})}
	m.mu.Lock()
	m.handle = handle
	m.pid = cmd.Process.Pid
	m.startedAt = time.Now()
	m.mu.Unlock()

	go func() {
		waitErr := cmd.Wait()
		if closeErr := logFile.Close(); closeErr != nil {
			config.Logger.Warn("[mihomo] close log file failed", "error", closeErr)
		}
		close(handle.done)
		m.mu.Lock()
		if m.handle == handle {
			m.running = false
			m.pid = 0
			if waitErr != nil {
				m.lastErr = fmt.Sprintf("mihomo 进程退出: %v（详见 data/mihomo/mihomo.log）", waitErr)
			}
		}
		m.mu.Unlock()
		if waitErr != nil {
			config.Logger.Warn("[mihomo] process exited", "error", waitErr)
		}
	}()

	return m.waitReady(handle, apiPort, 5*time.Second)
}

// waitReady 轮询 external-controller 端口直到可连接或进程退出。
func (m *Manager) waitReady(handle *procHandle, apiPort int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", apiPort)
	for time.Now().Before(deadline) {
		select {
		case <-handle.done:
			return fmt.Errorf("mihomo 启动后立即退出（详见 data/mihomo/mihomo.log）")
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			if closeErr := conn.Close(); closeErr != nil {
				config.Logger.Warn("[mihomo] close probe conn failed", "error", closeErr)
			}
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	// 进程仍存活但 API 未就绪：视为启动成功（部分构建端口开放较慢）。
	config.Logger.Warn("[mihomo] external-controller probe timed out; assuming process healthy")
	return nil
}

// stopProcess 终止当前记录的子进程并等待其退出（避免端口占用竞争）。
func (m *Manager) stopProcess() {
	m.mu.Lock()
	handle := m.handle
	m.handle = nil
	m.running = false
	m.pid = 0
	m.mu.Unlock()
	if handle == nil || handle.cmd == nil || handle.cmd.Process == nil {
		return
	}
	if err := handle.cmd.Process.Kill(); err != nil {
		config.Logger.Warn("[mihomo] kill process failed", "error", err)
	}
	select {
	case <-handle.done:
	case <-time.After(3 * time.Second):
		config.Logger.Warn("[mihomo] wait for process exit timed out")
	}
}

// resolveBinary 按优先级定位 mihomo 可执行文件：
// 配置 binary_path > MIHOMO_PATH 环境变量 > ds2api 同目录 > 工作目录
// （含 bin/ 子目录）> PATH。
func resolveBinary(configured string) (string, error) {
	exeName := "mihomo"
	if runtime.GOOS == "windows" {
		exeName = "mihomo.exe"
	}
	candidates := []string{}
	if v := strings.TrimSpace(configured); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(os.Getenv("MIHOMO_PATH")); v != "" {
		candidates = append(candidates, v)
	}
	if exePath, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exePath), exeName))
	}
	candidates = append(candidates,
		filepath.Join(config.BaseDir(), exeName),
		filepath.Join(config.BaseDir(), "bin", exeName),
	)
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return candidate, nil
			}
			return abs, nil
		}
	}
	if path, err := exec.LookPath("mihomo"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("未找到 mihomo 可执行文件：请将 %s 放到 ds2api 同级目录、工作目录或 bin/ 下，或设置 MIHOMO_PATH / mihomo.binary_path", exeName)
}

// preflightPorts 启动前检查节点监听端口与 API 端口未被占用，
// 给出比 mihomo 崩溃日志更友好的错误。
func preflightPorts(mcfg config.MihomoConfig, bindings []activeBinding) error {
	check := func(port int, purpose string) error {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return fmt.Errorf("端口 %d（%s）被占用，请调整 mihomo.base_port/api_port 或释放端口", port, purpose)
		}
		if closeErr := ln.Close(); closeErr != nil {
			config.Logger.Warn("[mihomo] close preflight listener failed", "error", closeErr)
		}
		return nil
	}
	for _, b := range bindings {
		if err := check(b.Port, "节点 "+b.NodeName); err != nil {
			return err
		}
	}
	return check(mcfg.APIPort, "external-controller")
}

// portListening 检测本地端口是否处于监听状态。
func portListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	if closeErr := conn.Close(); closeErr != nil {
		config.Logger.Warn("[mihomo] close probe conn failed", "error", closeErr)
	}
	return true
}

// writeFileAtomic 先写临时文件再 rename，避免半截配置。
func writeFileAtomic(path string, content []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		if removeErr := os.Remove(tmp); removeErr != nil {
			config.Logger.Warn("[mihomo] remove temp config failed", "error", removeErr)
		}
		return err
	}
	return nil
}
