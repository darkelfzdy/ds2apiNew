import { Link2, Loader2, X } from 'lucide-react'
import clsx from 'clsx'

import { useI18n } from '../../i18n'

// accountOptions 把 config.accounts 归一化为 {identifier, label}，
// identifier 与后端 Account.Identifier() 规则一致（email 优先，其次 mobile）。
export function accountOptions(accounts) {
    return (accounts || [])
        .map(acc => {
            const identifier = String(acc?.email || acc?.mobile || '').trim()
            if (!identifier) return null
            const name = String(acc?.name || '').trim()
            return { identifier, label: name ? `${name} (${identifier})` : identifier }
        })
        .filter(Boolean)
}

function PortBadge({ t, port }) {
    if (!port) {
        return (
            <span className="inline-flex items-center rounded-full border border-border bg-muted/20 px-2 py-1 text-[10px] font-medium text-muted-foreground">
                {t('mihomoBridge.portUnassigned')}
            </span>
        )
    }
    return (
        <span className="inline-flex items-center rounded-full border border-emerald-500/20 bg-emerald-500/10 px-2 py-1 text-[10px] font-mono font-medium text-emerald-500">
            127.0.0.1:{port}
        </span>
    )
}

function NodeRow({ t, node, options, busy, onBind, onUnbind }) {
    const bound = Array.isArray(node.accounts) ? node.accounts : []
    const boundIds = new Set(bound.map(acc => acc.identifier))
    const candidates = options.filter(opt => !boundIds.has(opt.identifier))
    const binding = Boolean(busy?.[`bind:${node.node_key}`])

    return (
        <div className="p-4 md:p-5 space-y-3 hover:bg-muted/40 transition-colors">
            <div className="flex flex-col lg:flex-row lg:items-center justify-between gap-3">
                <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                        <div className="font-medium text-foreground">{node.name}</div>
                        <span className="inline-flex items-center rounded-full border border-primary/20 bg-primary/10 px-2 py-1 text-[10px] font-medium uppercase tracking-wide text-primary">
                            {node.type || '?'}
                        </span>
                        <PortBadge t={t} port={node.local_port} />
                    </div>
                    <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                        {node.server && (
                            <span className="font-mono bg-muted/30 px-2 py-1 rounded border border-border">{node.server}</span>
                        )}
                        <span>{node.subscription}</span>
                    </div>
                </div>

                <div className="flex items-center gap-2 self-start lg:self-auto">
                    <Link2 className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
                    <select
                        className="input-field !w-auto min-w-[200px] text-xs"
                        value=""
                        disabled={binding || candidates.length === 0}
                        onChange={e => {
                            const identifier = e.target.value
                            if (identifier) onBind(identifier, node.node_key)
                        }}
                    >
                        <option value="">{t('mihomoBridge.bindPlaceholder')}</option>
                        {candidates.map(opt => (
                            <option key={opt.identifier} value={opt.identifier}>{opt.label}</option>
                        ))}
                    </select>
                    {binding && <Loader2 className="w-4 h-4 animate-spin text-primary shrink-0" />}
                </div>
            </div>

            {bound.length > 0 && (
                <div className="flex flex-wrap items-center gap-2">
                    <span className="text-[10px] font-bold uppercase tracking-wider text-muted-foreground">
                        {t('mihomoBridge.boundAccounts')}
                    </span>
                    {bound.map(acc => {
                        const unbinding = Boolean(busy?.[`bind:${acc.identifier}`])
                        return (
                            <span
                                key={acc.identifier}
                                className={clsx(
                                    'inline-flex items-center gap-1.5 rounded-full border border-emerald-500/25 bg-emerald-500/10 pl-3 pr-1.5 py-1 text-[11px] font-medium text-emerald-500',
                                    unbinding && 'opacity-60'
                                )}
                            >
                                {acc.label}
                                <button
                                    onClick={() => onUnbind(acc.identifier)}
                                    disabled={unbinding}
                                    className="rounded-full p-0.5 hover:bg-emerald-500/20 transition-colors disabled:opacity-50"
                                    title={t('mihomoBridge.unbindAction')}
                                >
                                    {unbinding ? <Loader2 className="w-3 h-3 animate-spin" /> : <X className="w-3 h-3" />}
                                </button>
                            </span>
                        )
                    })}
                </div>
            )}
        </div>
    )
}

export default function MihomoNodesTable({ nodes, accounts, busy, onBind, onUnbind }) {
    const { t } = useI18n()
    const options = accountOptions(accounts)

    return (
        <div className="bg-card border border-border rounded-xl overflow-hidden shadow-sm">
            <div className="p-6 border-b border-border">
                <h2 className="text-lg font-semibold">{t('mihomoBridge.nodesTitle')}</h2>
                <p className="text-sm text-muted-foreground mt-1">{t('mihomoBridge.nodesDesc')}</p>
            </div>

            {nodes.length === 0 ? (
                <div className="p-10 text-center text-muted-foreground">{t('mihomoBridge.noNodes')}</div>
            ) : (
                <div className="divide-y divide-border">
                    {nodes.map(node => (
                        <NodeRow
                            key={node.node_key}
                            t={t}
                            node={node}
                            options={options}
                            busy={busy}
                            onBind={onBind}
                            onUnbind={onUnbind}
                        />
                    ))}
                </div>
            )}
        </div>
    )
}
