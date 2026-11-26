// Task bar (bottom strip) and task center (modal, mockup C-4).
import { useState } from "preact/hooks";
import { Job, formatBytes, formatTime } from "./api";
import {
  IconCancel,
  IconCheck,
  IconClock,
  IconCopy,
  IconError,
  IconInfo,
  IconMove,
  IconRefresh,
  IconTrash,
  IconWarn,
} from "./icons";
import { errText, t } from "./i18n";
import { useApp } from "./store";

const actionIcon = (a: string) => {
  switch (a) {
    case "move":
      return <IconMove size={16} />;
    case "delete":
      return <IconTrash size={16} />;
    case "hash":
      return <IconRefresh size={16} />;
    default:
      return <IconCopy size={16} />;
  }
};

export function jobLabel(j: Job): string {
  const src = j.plan?.source?.path?.split("/").pop() ?? "";
  const dst = j.plan?.targetDir ?? "";
  switch (j.action) {
    case "copy":
      return `${t("act.copy")}: ${src} → ${dst || "…"}`;
    case "move":
      return `${t("act.move") || t("act.moveToPane")}: ${src} → ${dst || "…"}`;
    case "delete":
      return `${t("act.delete")}: ${src}`;
    case "hash":
      return `${t("act.hash")}: ${src}`;
    default:
      return j.action;
  }
}

export function stateLabel(s: string): string {
  switch (s) {
    case "queued":
      return t("task.queued");
    case "running":
      return t("task.runningState");
    case "verifying":
      return t("task.verifying");
    case "completed":
      return t("task.completed");
    case "cancelled":
      return t("task.cancelled");
    case "failed":
      return t("task.failedFinal");
    case "failed_retryable":
      return t("task.retryable");
    case "needs_attention":
      return t("task.attention");
    case "cancel_requested":
      return t("task.cancelRequested");
    default:
      return s;
  }
}

function pct(j: Job): number {
  if (j.action === "hash" || j.action === "delete") {
    return j.totalItems > 0 ? Math.min(100, Math.round((j.doneItems / j.totalItems) * 100)) : 0;
  }
  if (j.totalBytes > 0) return Math.min(100, Math.round((j.doneBytes / j.totalBytes) * 100));
  if (j.totalItems > 0) return Math.min(100, Math.round((j.doneItems / j.totalItems) * 100));
  return 0;
}

function progClass(j: Job): string {
  if (j.state === "completed") return "done";
  if (j.state === "failed" || j.state === "failed_retryable") return "fail";
  return "";
}
void progClass;

export function TaskBar({
  onOpenCenter,
  onConflict,
}: {
  onOpenCenter: () => void;
  onConflict: (j: Job) => void;
}) {
  const { jobs, refreshJobs, toast } = useApp();
  const [open, setOpen] = useState(true);
  const active = jobs.filter(
    (j) => j.state === "running" || j.state === "queued" || j.state === "verifying" || j.state === "cancel_requested",
  );
  const attention = jobs.filter((j) => j.state === "needs_attention");
  const recent = [...active, ...attention].slice(0, 4);
  if (recent.length === 0) return null;

  return (
    <div class="tv-taskbar" role="region" aria-label={t("task.bar")}>
      <div class="tv-taskbar-head">
        <span>{t("task.bar")}</span>
        <span class="tv-count">
          {active.length} {t("task.running")}
        </span>
        {attention.length > 0 && (
          <button class="tv-btn danger" onClick={() => onConflict(attention[0])}>
            <IconWarn size={13} />
            {t("task.resolveConflicts")} ({attention.length})
          </button>
        )}
        <span class="tv-spacer" />
        <button
          class="tv-btn ghost"
          onClick={() => {
            refreshJobs();
            onOpenCenter();
          }}
        >
          {t("task.center")}
        </button>
        <button class="tv-btn ghost" onClick={() => setOpen(!open)}>
          {open ? "▾" : "▸"}
        </button>
      </div>
      {open &&
        recent.map((j) => (
          <div class="tv-taskrow" key={j.id}>
            <span class="tv-ic">{actionIcon(j.action)}</span>
            <span class="lbl">{jobLabel(j)}</span>
            <div class="tv-progmini tv-prog" style="flex:none;width:180px">
              <i style={`width:${pct(j)}%`} />
            </div>
            <span class="tv-num">{pct(j)}%</span>
            {j.cancelable && (
              <button
                class="tv-btn ghost"
                title={t("task.cancel")}
                onClick={() => {
                  useAppCancel(j.id, toast);
                }}
              >
                <IconCancel size={13} />
              </button>
            )}
          </div>
        ))}
    </div>
  );
}

// small helper so the cancel button stays terse
import { api } from "./api";
async function useAppCancel(id: string, toast: (s: string, k?: "ok" | "err" | "warn") => void) {
  try {
    await api.cancelJob(id);
    toast(t("task.cancelRequested"));
  } catch (e) {
    toast(String(e), "err");
  }
}

export function TaskCenter({
  onClose,
  onConflict,
}: {
  onClose: () => void;
  onConflict: (j: Job) => void;
}) {
  const { jobs, toast } = useApp();
  const [filter, setFilter] = useState<"running" | "done" | "failed">("running");
  const [logs, setLogs] = useState<Record<string, string[]>>({});

  const running = jobs.filter((j) =>
    ["queued", "running", "verifying", "cancel_requested", "needs_attention"].includes(j.state),
  );
  const done = jobs.filter((j) => j.state === "completed" || j.state === "cancelled");
  const failed = jobs.filter((j) => j.state === "failed" || j.state === "failed_retryable");
  const list = filter === "running" ? running : filter === "done" ? done : failed;
  const totalSpeed = 0; // speed comes from daemon deltas (computed in JobRow in a later rev)
  void totalSpeed;

  return (
    <div class="tv-taskcenter-overlay" onClick={(ev: MouseEvent) => {
      if ((ev.target as HTMLElement).classList.contains("tv-taskcenter-overlay")) onClose();
    }}>
      <div class="tv-modal">
        <div class="tv-modal-head">
          <h3>{t("task.center")}</h3>
          <span class="sub">{t("task.hint")}</span>
          <span class="tv-spacer" />
          <button class="tv-btn ghost" onClick={onClose} aria-label="close">
            ✕
          </button>
        </div>
        <div class="tv-modal-body">
          <div class="tv-statsrow">
            <div class="tv-statcard">
              <IconInfo />
              <div>
                <div class="num">{running.length}</div>
                <div class="lbl">{t("task.running")}</div>
              </div>
            </div>
            <div class="tv-statcard ok">
              <IconCheck />
              <div>
                <div class="num">{done.length}</div>
                <div class="lbl">{t("task.done")}</div>
              </div>
            </div>
            <div class="tv-statcard fail">
              <IconError />
              <div>
                <div class="num">{failed.length}</div>
                <div class="lbl">{t("task.failed")}</div>
              </div>
            </div>
            <div class="tv-statcard">
              <IconClock />
              <div>
                <div class="lbl">{t("task.hint")}</div>
              </div>
            </div>
          </div>

          <div style="display:flex;gap:6px;margin-bottom:10px">
            {(["running", "done", "failed"] as const).map((f) => (
              <button
                key={f}
                class={"tv-btn seg" + (filter === f ? " active" : "")}
                onClick={() => setFilter(f)}
              >
                {f === "running"
                  ? `${t("task.running")} (${running.length})`
                  : f === "done"
                    ? `${t("task.done")} (${done.length})`
                    : `${t("task.failed")} (${failed.length})`}
              </button>
            ))}
          </div>

          {list.length === 0 && <div class="tv-state">—</div>}
          {list.map((j) => (
            <JobRow
              key={j.id}
              job={j}
              logs={logs[j.id]}
              onToggleLog={async () => {
                if (logs[j.id]) {
                  setLogs((l) => ({ ...l, [j.id]: [] }));
                  return;
                }
                try {
                  const r = await api.jobEvents(j.id, 0);
                  setLogs((l) => ({
                    ...l,
                    [j.id]: r.events.map((e) => `${e.at.slice(11, 19)} [${e.type}] ${e.text}`),
                  }));
                } catch (e) {
                  toast(String(e), "err");
                }
              }}
              onCancel={async () => {
                try {
                  await api.cancelJob(j.id);
                  toast(t("task.cancelRequested"));
                } catch (e) {
                  toast(String(e), "err");
                }
              }}
              onRetry={async () => {
                try {
                  await api.retryJob(j.id);
                  toast(t("msg.jobQueued"));
                } catch (e) {
                  toast(String(e), "err");
                }
              }}
              onConflict={() => onConflict(j)}
            />
          ))}
        </div>
      </div>
    </div>
  );
}

function JobRow(d: {
  job: Job;
  logs?: string[];
  onToggleLog: () => void;
  onCancel: () => void;
  onRetry: () => void;
  onConflict: () => void;
}) {
  const j = d.job;
  const src = j.plan?.source?.path || "—";
  const dst = j.plan?.targetDir || "—";
  const speed = j.doneBytes > 0 && j.startedAt ? "" : "";
  void speed;
  return (
    <div class="tv-jobrow">
      <div class="tv-jobrow-top">
        <span class="badge">
          {actionIcon(j.action)}
          {t("act." + (j.action === "hash" ? "hash" : j.action === "delete" ? "delete" : j.action === "move" ? "moveToPane" : "copy"))}
        </span>
        <div class="paths">
          <div class="p">
            {t("dlg.conflict.src")}: {src}
          </div>
          <div class="p">
            {t("dlg.plan.target")}: {dst}
          </div>
        </div>
        <div class="tv-prog" style="flex:1;max-width:280px">
          <i style={`width:${pct(j)}%`} />
        </div>
        <span class="tv-num">{pct(j)}%</span>
        <div class="ops">
          {j.state === "needs_attention" && (
            <button class="tv-btn danger" onClick={d.onConflict}>
              <IconWarn size={13} />
              {t("task.resolveConflicts")}
            </button>
          )}
          {j.cancelable && (
            <button class="tv-btn" onClick={d.onCancel}>
              <IconCancel size={13} />
              {t("task.cancel")}
            </button>
          )}
          {j.state === "failed_retryable" && (
            <button class="tv-btn primary" onClick={d.onRetry}>
              {t("task.retry")}
            </button>
          )}
          <button class="tv-btn ghost" onClick={d.onToggleLog}>
            {t("task.log")}
          </button>
        </div>
      </div>
      <div class="meta">
        <span>{stateLabel(j.state)}</span>
        <span>
          {formatBytes(j.doneBytes)} / {formatBytes(j.totalBytes)}
        </span>
        <span>
          {j.doneItems}/{j.totalItems}
        </span>
        <span>{formatTime(j.createdAt)}</span>
        {j.errorCode && <span style="color:var(--tv-red)">{errText(j.errorCode)}</span>}
        {j.message && <span>{j.message}</span>}
      </div>
      {(j.warnings?.length ?? 0) > 0 && (
        <div class="tv-jobwarn">
          <IconWarn size={12} /> {j.warnings![j.warnings!.length - 1]}
        </div>
      )}
      {d.logs && d.logs.length > 0 && (
        <div class="tv-joblog">{d.logs.join("\n")}</div>
      )}
    </div>
  );
}
