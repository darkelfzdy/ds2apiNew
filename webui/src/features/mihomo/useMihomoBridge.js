import { useCallback, useEffect, useState } from 'react'

// useMihomoBridge 封装 Mihomo 代理桥管理接口的全部数据访问。
// config 变更（账号 proxy_id、托管代理列表）通过 onConfigChanged 通知上层刷新。
export default function useMihomoBridge({ authFetch, onMessage, onConfigChanged, t }) {
    const apiFetch = authFetch || fetch
    const [status, setStatus] = useState(null)
    const [subscriptions, setSubscriptions] = useState([])
    const [nodes, setNodes] = useState([])
    const [loading, setLoading] = useState(true)
    const [busy, setBusy] = useState({})

    const readApiResponse = useCallback(async (res) => {
        const contentType = String(res.headers.get('content-type') || '').toLowerCase()
        const raw = (await res.text()).trim()
        if (!raw) return {}
        if (contentType.includes('application/json')) {
            try {
                return JSON.parse(raw)
            } catch (_err) {
                return { detail: raw }
            }
        }
        return { detail: raw }
    }, [])

    const report = useCallback((res, data, successText) => {
        if (!res.ok) {
            onMessage('error', data.detail || t('messages.requestFailed'))
            return false
        }
        if (successText) onMessage('success', successText)
        return true
    }, [onMessage, t])

    const loadStatus = useCallback(async () => {
        try {
            const res = await apiFetch('/admin/mihomo/status')
            setStatus(await readApiResponse(res))
        } catch (_err) {
            setStatus(null)
        }
    }, [apiFetch, readApiResponse])

    const loadSubscriptions = useCallback(async () => {
        try {
            const res = await apiFetch('/admin/mihomo/subscriptions')
            const data = await readApiResponse(res)
            setSubscriptions(data.items || [])
        } catch (_err) {
            setSubscriptions([])
        }
    }, [apiFetch, readApiResponse])

    const loadNodes = useCallback(async () => {
        try {
            const res = await apiFetch('/admin/mihomo/nodes')
            const data = await readApiResponse(res)
            setNodes(data.items || [])
        } catch (_err) {
            setNodes([])
        }
    }, [apiFetch, readApiResponse])

    const loadAll = useCallback(async () => {
        setLoading(true)
        await Promise.all([loadStatus(), loadSubscriptions(), loadNodes()])
        setLoading(false)
    }, [loadStatus, loadSubscriptions, loadNodes])

    useEffect(() => {
        loadAll()
    }, [loadAll])

    const withBusy = useCallback(async (key, fn) => {
        setBusy(prev => ({ ...prev, [key]: true }))
        try {
            return await fn()
        } catch (err) {
            onMessage('error', err?.message || t('messages.networkError'))
            return false
        } finally {
            setBusy(prev => ({ ...prev, [key]: false }))
        }
    }, [onMessage, t])

    const saveSettings = useCallback((form) => withBusy('settings', async () => {
        const res = await apiFetch('/admin/mihomo/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                enabled: Boolean(form.enabled),
                binary_path: form.binary_path || '',
                base_port: Number(form.base_port) || 0,
                api_port: Number(form.api_port) || 0,
            }),
        })
        const data = await readApiResponse(res)
        if (!report(res, data, t('mihomoBridge.settingsSaved'))) return false
        if (data.status) setStatus(data.status)
        await onConfigChanged?.()
        return true
    }), [apiFetch, withBusy, readApiResponse, report, t, onConfigChanged])

    const applyNow = useCallback(() => withBusy('apply', async () => {
        const res = await apiFetch('/admin/mihomo/apply', { method: 'POST' })
        const data = await readApiResponse(res)
        if (!report(res, data, t('mihomoBridge.applySuccess'))) return false
        if (data.status) setStatus(data.status)
        return true
    }), [apiFetch, withBusy, readApiResponse, report, t])

    const addSubscription = useCallback((name, url) => withBusy('addSub', async () => {
        const res = await apiFetch('/admin/mihomo/subscriptions', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name, url }),
        })
        const data = await readApiResponse(res)
        const count = data?.subscription?.node_count ?? 0
        if (!report(res, data, t('mihomoBridge.addSuccess', { count }))) return false
        await Promise.all([loadSubscriptions(), loadNodes(), loadStatus()])
        await onConfigChanged?.()
        return true
    }), [apiFetch, withBusy, readApiResponse, report, t, loadSubscriptions, loadNodes, loadStatus, onConfigChanged])

    const refreshSubscription = useCallback((subID) => withBusy(`refresh:${subID}`, async () => {
        const res = await apiFetch(`/admin/mihomo/subscriptions/${encodeURIComponent(subID)}/refresh`, { method: 'POST' })
        const data = await readApiResponse(res)
        if (!report(res, data, t('mihomoBridge.refreshSuccess', { count: data.node_count ?? 0 }))) return false
        await Promise.all([loadSubscriptions(), loadNodes(), loadStatus()])
        await onConfigChanged?.()
        return true
    }), [apiFetch, withBusy, readApiResponse, report, t, loadSubscriptions, loadNodes, loadStatus, onConfigChanged])

    const deleteSubscription = useCallback((subID) => withBusy(`delete:${subID}`, async () => {
        const res = await apiFetch(`/admin/mihomo/subscriptions/${encodeURIComponent(subID)}`, { method: 'DELETE' })
        const data = await readApiResponse(res)
        if (!report(res, data, t('mihomoBridge.deleteSuccess'))) return false
        await Promise.all([loadSubscriptions(), loadNodes(), loadStatus()])
        await onConfigChanged?.()
        return true
    }), [apiFetch, withBusy, readApiResponse, report, t, loadSubscriptions, loadNodes, loadStatus, onConfigChanged])

    const bindAccount = useCallback((identifier, nodeKey) => withBusy(`bind:${nodeKey || identifier}`, async () => {
        const res = await apiFetch(`/admin/mihomo/bindings/${encodeURIComponent(identifier)}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ node: nodeKey || '' }),
        })
        const data = await readApiResponse(res)
        const okText = nodeKey ? t('mihomoBridge.bindSuccess') : t('mihomoBridge.unbindSuccess')
        if (!report(res, data, okText)) return false
        await Promise.all([loadNodes(), loadStatus()])
        await onConfigChanged?.()
        return true
    }), [apiFetch, withBusy, readApiResponse, report, t, loadNodes, loadStatus, onConfigChanged])

    return {
        status, subscriptions, nodes, loading, busy,
        reload: loadAll,
        saveSettings, applyNow,
        addSubscription, refreshSubscription, deleteSubscription,
        bindAccount,
    }
}
