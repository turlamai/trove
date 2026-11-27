// Dialogs: input, confirm-delete, plan preview, conflict resolution,
// namespace-mix block, ack. Text contracts follow specification §3.4/§D.5/D.6.
import { useEffect, useState } from "preact/hooks";
import {
  api,
  Conflict,
  Entry,
  formatBytes,
  formatTime,
  Job,
  Node,
  Plan,
  RootKind,
  ShareLink,
} from "./api";
import {
  IconCheck,
  IconCopy,
  IconError,
  IconFolder,
  IconInfo,
  IconLink,
  IconMove,
  IconShield,
  IconTrash,
  IconWarn,
} from "./icons";
import { errText, t } from "./i18n";
import { useApp } from "./store";

interface DialogChrome {
  children: preact.ComponentChildren;
  title: string;
  icon?: preact.ComponentChildren;
  wide?: boolean;
  onClose?: () => void;
  foot?: preact.ComponentChildren;
}

function Chrome(d: DialogChrome) {
  return (
    <div
      class="tv-dialog-overlay"
      onKeyDown={(ev: KeyboardEvent) => {
        if (ev.key === "Escape" && d.onClose) d.onClose();
      }}
    >
      <div class={"tv-dialog" + (d.wide ? " wide" : "")} role="dialog" aria-modal="true">
        <div class="tv-dialog-head">
          <h3>
            {d.icon}
            {d.title}
          </h3>
          <span class="tv-spacer" />
          {d.onClose && (
            <button class="tv-btn ghost" onClick={d.onClose} aria-label="close">
              ✕
            </button>
          )}
        </div>
        <div class="tv-dialog-body">{d.children}</div>
        {d.foot && <div class="tv-dialog-foot">{d.foot}</div>}
      </div>
    </div>
  );
}

export interface NewFolderDialogProps {
  onClose: () => void;
  onCreate: (name: string) => void;
}
export function NewFolderDialog(p: NewFolderDialogProps) {
  const [name, setName] = useState("");
  return (
    <Chrome
      title={t("dlg.newFolder.title")}
      icon={<IconFolder />}
      onClose={p.onClose}
      foot={
        <>
          <span class="tv-spacer" />
          <button class="tv-btn" onClick={p.onClose}>
            ✕
          </button>
          <button
            class="tv-btn primary"
            disabled={!name.trim()}
            onClick={() => p.onCreate(name.trim())}
          >
            <IconCheck size={14} />
            OK
          </button>
        </>
      }
    >
      <div class="tv-field">
        <input
          class="tv-input"
          autoFocus
          value={name}
          onKeyDown={(ev: KeyboardEvent) => {
            if (ev.key === "Enter" && name.trim()) p.onCreate(name.trim());
          }}
          onInput={(ev: InputEvent) => setName((ev.target as HTMLInputElement).value)}
        />
      </div>
    </Chrome>
  );
}

export interface RenameDialogProps {
  entry: Entry;
  onClose: () => void;
  onRename: (name: string) => void;
}
export function RenameDialog(p: RenameDialogProps) {
  const [name, setName] = useState(p.entry.name);
  return (
    <Chrome
      title={t("dlg.rename.title")}
      icon={<IconFolder />}
      onClose={p.onClose}
      foot={
        <>
          <span class="tv-spacer" />
          <button class="tv-btn" onClick={p.onClose}>
            ✕
          </button>
          <button
            class="tv-btn primary"
            disabled={!name.trim() || name === p.entry.name}
            onClick={() => p.onRename(name.trim())}
          >
            <IconCheck size={14} />
            OK
          </button>
        </>
      }
    >
      <div class="tv-field">
        <input
          class="tv-input"
          autoFocus
          onFocus={(ev: FocusEvent) => {
            const el = ev.target as HTMLInputElement;
            const dot = el.value.lastIndexOf(".");
            el.setSelectionRange(0, dot > 0 ? dot : el.value.length);
          }}
          value={name}
          onKeyDown={(ev: KeyboardEvent) => {
            if (ev.key === "Enter" && name.trim() && name !== p.entry.name)
              p.onRename(name.trim());
          }}
          onInput={(ev: InputEvent) => setName((ev.target as HTMLInputElement).value)}
        />
      </div>
    </Chrome>
  );
}

export interface ConfirmDeleteProps {
  count: number;
  bytes: number;
  protectedShare: boolean;
  onClose: () => void;
  onConfirm: () => void;
}
export function ConfirmDeleteDialog(p: ConfirmDeleteProps) {
  return (
    <Chrome
      title={t("dlg.confirmDelete.title")}
      icon={<IconError />}
      onClose={p.onClose}
      foot={
        <>
          <span class="tv-spacer" />
          <button class="tv-btn" onClick={p.onClose} autoFocus>
            ✕ {t("dlg.mix.close")}
          </button>
          <button class="tv-btn danger" onClick={p.onConfirm}>
            <IconError size={14} />
            {t("act.delete")}
          </button>
        </>
      }
    >
      <div class="tv-alert danger">
        <IconError />
        <span class="txt">{t("dlg.confirmDelete.body", { n: p.count, bytes: formatBytes(p.bytes) })}</span>
      </div>
      {p.protectedShare && (
        <div class="tv-alert warn">
          <IconWarn />
          <span class="txt">{t("dlg.confirmDelete.protected")}</span>
        </div>
      )}
    </Chrome>
  );
}

export interface PlanDialogProps {
  plan: Plan;
  action: string; // copy | move | delete | hash
  targetLabel: string;
  onClose: () => void;
  onConfirm: (policy: string) => void;
}
export function PlanDialog(p: PlanDialogProps) {
  const [policy, setPolicy] = useState("ask");
  const [ack, setAck] = useState(false);
  const needAck = !!p.plan.needsAck;
  return (
    <Chrome
      wide
      title={t("dlg.plan.title")}
      icon={<IconShield />}
      onClose={p.onClose}
      foot={
        <>
          <span class="left">
            {needAck && (
              <label class="tv-switchline" style="margin:0">
                <input
                  type="checkbox"
                  class="tv-checkbox"
                  checked={ack}
                  onChange={(ev: Event) => setAck((ev.target as HTMLInputElement).checked)}
                />
                <span class="hint">{t("dlg.plan.ack")}</span>
              </label>
            )}
          </span>
          <button class="tv-btn" onClick={p.onClose} autoFocus>
            ✕
          </button>
          <button
            class="tv-btn primary"
            disabled={needAck && !ack}
            onClick={() => p.onConfirm(policy)}
          >
            {p.action === "move" ? <IconMove size={14} /> : <IconCopy size={14} />}
            {p.action === "copy"
              ? t("dlg.plan.startCopy")
              : p.action === "move"
                ? t("dlg.plan.startMove")
                : t("act.delete")}
          </button>
        </>
      }
    >
      <div class="tv-plan-grid">
        <div class="tv-plan-card">
          <div class="h">
            <IconInfo size={14} />
            {t("dlg.plan.items")}
          </div>
          <div class="tv-kv">
            <span class="k">{t("dlg.plan.files")}</span>
            <span>{p.plan.files}</span>
            <span class="k">{t("dlg.plan.dirs")}</span>
            <span>{p.plan.dirs}</span>
            <span class="k">{t("dlg.plan.bytes")}</span>
            <span>
              {formatBytes(p.plan.bytes)} {p.plan.estimate ? t("dlg.plan.estimate") : ""}
            </span>
          </div>
        </div>
        <div class="tv-plan-card">
          <div class="h">
            <IconFolder size={14} />
            {t("dlg.plan.target")}
          </div>
          <div class="p">{p.targetLabel}</div>
          {p.action === "copy" || p.action === "move" ? (
            <div class="tv-field">
              <label>{t("dlg.plan.policy")}</label>
              <select
                class="tv-input"
                value={policy}
                onChange={(ev: Event) => setPolicy((ev.target as HTMLSelectElement).value)}
              >
                <option value="ask">{t("dlg.conflict.notice")}</option>
                <option value="skip">{t("dlg.conflict.skip")}</option>
                <option value="rename">{t("dlg.conflict.rename")}</option>
                <option value="overwrite">{t("dlg.conflict.overwrite")}</option>
              </select>
            </div>
          ) : null}
        </div>
      </div>
      {(p.plan.conflicts?.length ?? 0) > 0 && (
        <div class="tv-alert warn">
          <IconWarn />
          <span class="txt">
            {t("dlg.conflict.notice")} ({p.plan.conflicts!.length})
          </span>
        </div>
      )}
      {(p.plan.risks?.length ?? 0) > 0 && (
        <div>
          <b>{t("dlg.plan.risk")}</b>
          <ul class="tv-risklist">
            {dedupeRisks(p.plan.risks ?? []).map((r) => (
              <li>
                <IconWarn size={13} />
                {r.note} ({r.code})
              </li>
            ))}
          </ul>
        </div>
      )}
    </Chrome>
  );
}

function dedupeRisks(risks: { code: string; note: string }[]) {
  const seen = new Map<string, string>();
  for (const r of risks) seen.set(r.code, r.note);
  return [...seen.entries()].map(([code, note]) => ({ code, note }));
}

export interface AckDialogProps {
  onClose: () => void;
  onAck: () => void;
}
export function AckDialog(p: AckDialogProps) {
  const [ack, setAck] = useState(false);
  return (
    <Chrome
      title={t("dlg.plan.title")}
      icon={<IconShield />}
      onClose={p.onClose}
      foot={
        <>
          <span class="tv-spacer" />
          <button class="tv-btn" onClick={p.onClose}>
            ✕
          </button>
          <button class="tv-btn primary" disabled={!ack} onClick={p.onAck}>
            <IconCheck size={14} />
            OK
          </button>
        </>
      }
    >
      <div class="tv-alert warn">
        <IconWarn />
        <span class="txt">{t("dlg.confirmDelete.protected")}</span>
      </div>
      <label class="tv-switchline">
        <input
          type="checkbox"
          class="tv-checkbox"
          checked={ack}
          onChange={(ev: Event) => setAck((ev.target as HTMLInputElement).checked)}
        />
        <span class="hint">{t("dlg.plan.ack")}</span>
      </label>
    </Chrome>
  );
}

export interface MixDialogProps {
  onClose: () => void;
  srcLabel: string;
  dstLabel: string;
}
export function MixDialog(p: MixDialogProps) {
  return (
    <Chrome
      wide
      title={t("dlg.mix.title")}
      icon={<IconShield />}
      onClose={p.onClose}
      foot={
        <>
          <span class="tv-spacer" />
          <button class="tv-btn primary" onClick={p.onClose} autoFocus>
            {t("dlg.mix.close")}
          </button>
        </>
      }
    >
      <div class="tv-alert danger">
        <IconError />
        <span class="txt">
          <b>{t("dlg.mix.title")}</b> — {t("err.NAMESPACE_MIX_FORBIDDEN")}
        </span>
      </div>
      <div class="tv-mix-grid">
        <div class="tv-mix-card src">
          <div class="h">
            <IconFolder size={14} /> /mnt/user
          </div>
          <div class="p">{p.srcLabel}</div>
        </div>
        <div class="tv-mix-arrow">
          <IconError size={22} />
        </div>
        <div class="tv-mix-card dstc">
          <div class="h">
            <IconFolder size={14} /> /mnt/diskN · /mnt/&lt;pool&gt;
          </div>
          <div class="p">{p.dstLabel}</div>
        </div>
      </div>
      <div class="tv-alert info">
        <IconInfo />
        <span class="txt">{t("dlg.mix.body")}</span>
      </div>
      <div class="tv-alert info">
        <IconInfo />
        <span class="txt">{t("dlg.mix.hint")}</span>
      </div>
    </Chrome>
  );
}

export interface ConflictDialogProps {
  job: Job;
  onClose: () => void;
  onResolve: (decisions: { path: string; resolution: string }[]) => void;
}
export function ConflictDialog(p: ConflictDialogProps) {
  const pending = (p.job.conflicts ?? []).filter((c) => !c.resolved);
  const [decisions, setDecisions] = useState<Record<string, string>>({});
  const [applyAll, setApplyAll] = useState("");
  const decided = Object.keys(decisions).length;

  const decideAll = (resolution: string) => {
    const d: Record<string, string> = {};
    pending.forEach((c) => (d[c.path] = resolution));
    setDecisions(d);
  };

  return (
    <Chrome
      wide
      title={t("dlg.conflict.title")}
      icon={<IconWarn />}
      onClose={p.onClose}
      foot={
        <>
          <span class="left">
            <select
              class="tv-input"
              style="width:auto"
              value={applyAll}
              onChange={(ev: Event) => setApplyAll((ev.target as HTMLSelectElement).value)}
            >
              <option value="">{t("dlg.conflict.applyAll")}</option>
              <option value="skip">{t("dlg.conflict.skip")}</option>
              <option value="rename">{t("dlg.conflict.rename")}</option>
              <option value="overwrite">{t("dlg.conflict.overwrite")}</option>
            </select>
            {applyAll && (
              <button class="tv-btn" onClick={() => decideAll(applyAll)}>
                {t("dlg.conflict.applyAll")}
              </button>
            )}
          </span>
          <button class="tv-btn" onClick={p.onClose}>
            ✕
          </button>
          <button
            class="tv-btn primary"
            disabled={decided === 0}
            onClick={() =>
              p.onResolve(
                Object.entries(decisions).map(([path, resolution]) => ({ path, resolution })),
              )
            }
          >
            <IconCheck size={14} />
            OK
          </button>
        </>
      }
    >
      <div class="tv-alert warn">
        <IconWarn />
        <span class="txt">{t("dlg.conflict.notice")}</span>
      </div>
      {pending.map((c) => (
        <ConflictItem
          key={c.path}
          conflict={c}
          decision={decisions[c.path]}
          onDecide={(res) => setDecisions((d) => ({ ...d, [c.path]: res }))}
        />
      ))}
    </Chrome>
  );
}

function ConflictItem(d: { conflict: Conflict; decision?: string; onDecide: (r: string) => void }) {
  const c = d.conflict;
  return (
    <div class="tv-conflict-item">
      <div class="path">{c.path}</div>
      <div class="tv-conflict-cols">
        <div class="src">
          <div class="h">
            <IconMove size={13} />
            {t("dlg.conflict.src")}
          </div>
          <div class="kv tv-kv">
            <span class="k">{t("dlg.conflict.size")}</span>
            <span>{formatBytes(c.srcSize)}</span>
            <span class="k">{t("dlg.conflict.mtime")}</span>
            <span>{formatTime(c.srcMtime)}</span>
          </div>
        </div>
        <div class="dstc">
          <div class="h">
            <IconCopy size={13} />
            {t("dlg.conflict.dst")}
          </div>
          <div class="kv tv-kv">
            <span class="k">{t("dlg.conflict.size")}</span>
            <span>{formatBytes(c.dstSize)}</span>
            <span class="k">{t("dlg.conflict.mtime")}</span>
            <span>{formatTime(c.dstMtime)}</span>
          </div>
        </div>
      </div>
      <div class="ops">
        <button
          class={"tv-btn" + (d.decision === "skip" ? " seg active" : "")}
          onClick={() => d.onDecide("skip")}
        >
          {t("dlg.conflict.skip")}
        </button>
        <button
          class={"tv-btn" + (d.decision === "overwrite" ? " seg active" : "")}
          onClick={() => d.onDecide("overwrite")}
        >
          {t("dlg.conflict.overwrite")}
        </button>
        <button
          class={"tv-btn" + (d.decision === "rename" ? " seg active" : "")}
          onClick={() => d.onDecide("rename")}
        >
          {t("dlg.conflict.rename")}
        </button>
        {d.decision && (
          <span class="done">
            <IconCheck size={13} /> {errText("CONFLICT_REQUIRES_DECISION") === "" ? "" : "✓"}
          </span>
        )}
      </div>
    </div>
  );
}

export interface TrashDialogProps {
  rootId: string;
  onClose: () => void;
  onChanged: () => void;
}
export function TrashDialog(p: TrashDialogProps) {
  const { toast, toastErr } = useApp();
  const [entries, setEntries] = useState<
    { name: string; originRel: string; kind: string; size: number; deletedAt: string }[] | null
  >(null);
  const [busy, setBusy] = useState(false);
  const load = async () => {
    try {
      const r = await api.trashList(p.rootId);
      setEntries(r.entries);
    } catch (e) {
      toastErr(e);
    }
  };
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.rootId]);
  const act = async (fn: () => Promise<unknown>, okText: string) => {
    setBusy(true);
    try {
      await fn();
      toast(okText);
      await load();
      p.onChanged();
    } catch (e) {
      toastErr(e);
    }
    setBusy(false);
  };
  return (
    <Chrome
      wide
      title={t("nav.trash")}
      icon={<IconTrash />}
      onClose={p.onClose}
      foot={
        <>
          <span class="tv-spacer" />
          <button
            class="tv-btn danger"
            disabled={busy || !entries || entries.length === 0}
            onClick={() =>
              act(async () => {
                if (!entries) return;
                const plan = await api.plan({
                  action: "delete",
                  targets: entries.map((e) => ({
                    root: parseRootId2(p.rootId),
                    path: ".trove-trash/" + e.name,
                  })),
                  purge: true,
                  conflictPolicy: "skip",
                });
                await api.submitJob(plan.id, plan.token);
              }, t("trash.purgeAll"))
            }
          >
            {t("trash.purgeAll")}
          </button>
        </>
      }
    >
      {entries === null ? (
        <div class="tv-spinner" />
      ) : entries.length === 0 ? (
        <div class="tv-state">{t("trash.empty")}</div>
      ) : (
        <table class="tv-table">
          <thead>
            <tr>
              <th>{t("trash.name")}</th>
              <th style="width:110px">{t("col.size")}</th>
              <th style="width:150px">{t("trash.deletedAt")}</th>
              <th style="width:190px" />
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.name} class="row">
                <td>
                  <div class="tv-cellname">
                    <span class="nm" title={e.name}>{e.name}</span>
                    <span class="tgt">{e.originRel || "—"}</span>
                  </div>
                </td>
                <td class="tv-num">{formatBytes(e.size)}</td>
                <td class="tv-num">{formatTime(e.deletedAt)}</td>
                <td>
                  <div style="display:flex;gap:6px;justify-content:flex-end">
                    <button
                      class="tv-btn"
                      disabled={busy}
                      onClick={() =>
                        act(async () => {
                          await api.trashRestore(p.rootId, e.name);
                        }, t("trash.restored"))
                      }
                    >
                      {t("trash.restore")}
                    </button>
                    <button
                      class="tv-btn danger"
                      disabled={busy}
                      onClick={() =>
                        act(async () => {
                          const plan = await api.plan({
                            action: "delete",
                            targets: [{ root: parseRootId2(p.rootId), path: ".trove-trash/" + e.name }],
                            purge: true,
                            conflictPolicy: "skip",
                          });
                          await api.submitJob(plan.id, plan.token);
                        }, t("trash.purge"))
                      }
                    >
                      {t("trash.purge")}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Chrome>
  );
}

function parseRootId2(rootId: string): { kind: RootKind; id: string } {
  const [kind, id] = rootId.split(":");
  return { kind: kind as RootKind, id };
}

export interface ShareDialogProps {
  rootId: string;
  path: string;
  onClose: () => void;
}
export function ShareDialog(p: ShareDialogProps) {
  const { toast, toastErr, caps } = useApp();
  const [links, setLinks] = useState<ShareLink[] | null>(null);
  const [expiryDays, setExpiryDays] = useState(7);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [sharePort, setSharePort] = useState<number | null>(null);

  const load = async () => {
    try {
      const r = await api.sharesList();
      setLinks((r.shares || []).filter(x => x.root.kind + ":" + x.root.id === p.rootId && x.relPath === p.path));
    } catch { /* ignore */ }
    try {
      const st = await api.settings();
      if (st.shareEnable) setSharePort(st.sharePort);
    } catch { /* ignore */ }
  };
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.rootId, p.path]);

  const shareUrl = (token: string) => {
    const host = window.location.hostname;
    const proto = window.location.protocol;
    const port = sharePort ?? 8787;
    const suffix = port !== 1996 ? ":" + port : "";
    return `${proto}//${host}${suffix}/s/${token}`;
  };

  const create = async () => {
    setBusy(true);
    try {
      await api.sharesCreate({ rootId: p.rootId, path: p.path, expiryDays, password });
      toast(t("share.created"));
      await load();
    } catch (e) {
      toastErr(e);
    }
    setBusy(false);
  };
  const revoke = async (token: string) => {
    setBusy(true);
    try {
      await api.sharesRevoke(token);
      await load();
    } catch (e) {
      toastErr(e);
    }
    setBusy(false);
  };
  const copyLink = async (token: string) => {
    try {
      await navigator.clipboard.writeText(shareUrl(token));
      toast(t("msg.copied"));
    } catch {
      toast(shareUrl(token), "warn");
    }
  };

  const name = p.path.split("/").pop() || "";
  return (
    <Chrome
      title={t("share.title")}
      icon={<IconLink />}
      onClose={p.onClose}
      foot={<><span class="tv-spacer" /><button class="tv-btn" onClick={p.onClose}>✕</button></>}
    >
      <div class="tv-alert info">
        <IconInfo />
        <span class="txt">
          {t("share.desc", { name })}
          {caps && !caps.writeOk && <b> — {t("err.JOB_STORAGE_UNAVAILABLE")}</b>}
        </span>
      </div>

      <div class="tv-kv" style="margin-bottom:10px">
        <span class="k">{t("share.expiry")}</span>
        <span>
          <select class="tv-input" style="max-width:180px" value={String(expiryDays)}
            onChange={(ev: Event) => setExpiryDays(Number((ev.target as HTMLSelectElement).value))}>
            <option value="1">{t("share.expiry.1d")}</option>
            <option value="7">{t("share.expiry.7d")}</option>
            <option value="30">{t("share.expiry.30d")}</option>
            <option value="0">{t("share.expiry.never")}</option>
          </select>
        </span>
        <span class="k">{t("share.password")}</span>
        <span><input class="tv-input" style="max-width:220px" type="password" value={password}
          placeholder={t("share.password.optional")}
          onInput={(ev: InputEvent) => setPassword((ev.target as HTMLInputElement).value)} /></span>
      </div>
      <div style="margin-bottom:14px">
        <button class="tv-btn primary" disabled={busy} onClick={() => void create()}>
          <IconLink size={13} />
          {t("share.create")}
        </button>
      </div>

      <h4 style="font-size:12px;color:var(--tv-text-muted);margin:14px 0 8px">
        {t("share.active")} ({links?.length ?? 0})
      </h4>
      {links && links.length > 0 && (
        <table class="tv-table">
          <thead><tr>
            <th>{t("share.token")}</th>
            <th style="width:110px">{t("share.expiry")}</th>
            <th style="width:80px">{t("share.downloads")}</th>
            <th style="width:220px" />
          </tr></thead>
          <tbody>
            {links.map((l) => (
              <tr key={l.token} class="row">
                <td style="font-family:var(--tv-mono);font-size:11px">
                  …{l.token.slice(-12)}{l.passwordHash ? " 🔒" : ""}
                </td>
                <td class="tv-num">{l.expiresAt ? formatTime(l.expiresAt) : "∞"}</td>
                <td class="tv-num">{l.downloads}</td>
                <td>
                  <div style="display:flex;gap:6px;justify-content:flex-end">
                    <button class="tv-btn" onClick={() => void copyLink(l.token)}>
                      <IconCopy size={13} /> {t("share.copy")}
                    </button>
                    <button class="tv-btn danger" disabled={busy} onClick={() => void revoke(l.token)}>
                      {t("share.revoke")}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {sharePort !== null && (
        <div class="tv-alert warn" style="margin-top:10px">
          <IconWarn size={13} />
          <span class="txt">{t("share.portNote", { port: String(sharePort) })}</span>
        </div>
      )}
    </Chrome>
  );
}

export function JobErrorDialog(d: { job: Job; onClose: () => void; onRetry: () => void }) {
  return (
    <Chrome
      title={t("task.failed")}
      icon={<IconError />}
      onClose={d.onClose}
      foot={
        <>
          <span class="tv-spacer" />
          <button class="tv-btn" onClick={d.onClose}>
            ✕
          </button>
          <button class="tv-btn primary" onClick={d.onRetry}>
            {t("task.retry")}
          </button>
        </>
      }
    >
      <div class="tv-alert danger">
        <IconError />
        <span class="txt">
          <b>{d.job.errorCode}</b> — {d.job.message || errText(d.job.errorCode || "")}
        </span>
      </div>
      {(d.job.warnings?.length ?? 0) > 0 && (
        <ul class="tv-risklist">
          {d.job.warnings!.map((w, i) => (
            <li key={i}>
              <IconWarn size={13} />
              {w}
            </li>
          ))}
        </ul>
      )}
    </Chrome>
  );
}

export { Chrome as DialogChrome };
export type { Node as PlanNode };
