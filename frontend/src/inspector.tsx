// Inspector sidebar (mockup C-3): verified metadata only — physical
// location is shown as the namespace it came from; anything unverifiable
// stays "unknown" (specification §D.3).
import { useEffect, useState } from "preact/hooks";
import {
  api,
  Entry,
  EntryDetail,
  formatBytes,
  formatTime,
  TroveError,
} from "./api";
import {
  IconCheck,
  IconClose,
  IconCopy,
  IconDisk,
  IconFolder,
  IconFor,
  IconHash,
  IconLink,
  IconMove,
  IconPencil,
  IconSSD,
  IconTrash,
  iconClassForName,
} from "./icons";
import { errText, t } from "./i18n";
import { useApp } from "./store";

export interface InspectorTarget {
  rootId: string;
  path: string; // parent dir
  entry: Entry;
}

export function Inspector({
  target,
  onClose,
  onRename,
  onDelete,
  onMove,
  onShare,
}: {
  target: InspectorTarget | null;
  onClose: () => void;
  onRename: (e: Entry) => void;
  onDelete: (e: Entry) => void;
  onMove: (e: Entry) => void;
  onShare?: (t: InspectorTarget) => void;
}) {
  const { toast } = useApp();
  const [detail, setDetail] = useState<EntryDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [hash, setHash] = useState<{ state: "idle" | "running" | "done" | "error"; value?: string; jobId?: string }>({
    state: "idle",
  });
  const [preview, setPreview] = useState<{
    kind: "image" | "text" | "pdf" | "video" | "audio" | null;
    url?: string;
    text?: string;
  }>({ kind: null });

  useEffect(() => {
    setDetail(null);
    setErr(null);
    setHash({ state: "idle" });
    setPreview({ kind: null });
    if (!target) return;
    api
      .entry(target.rootId, target.path ? `${target.path}/${target.entry.name}` : target.entry.name)
      .then(setDetail)
      .catch((e: unknown) => setErr(e instanceof TroveError ? e.code : "IO_FAILURE"));
  }, [target?.rootId, target?.path, target?.entry.name]);

  // 预览：图片/文本/PDF 走 blob（不执行任何内容；文本有大小封顶）
  useEffect(() => {
    if (!target || err) return;
    const entryName = target.entry.name.toLowerCase();
    const isImg = /\.(jpe?g|png|gif|webp|bmp)$/i.test(entryName);
    const isTxt = /\.(txt|md|log|json|xml|csv|srt|nfo|ini|cfg|yaml|yml|toml)$/i.test(entryName);
    const isPdf = entryName.endsWith(".pdf");
    const isVideo = /\.(mp4|webm|mkv|mov|m4v)$/i.test(entryName);
    const isAudio = /\.(mp3|flac|m4a|wav|ogg)$/i.test(entryName);
    if (!isImg && !isTxt && !isPdf && !isVideo && !isAudio) return;
    if (isTxt && target.entry.size > 262144) return;
    if ((isImg || isPdf) && target.entry.size > 33554432) return;
    if ((isVideo || isAudio) && target.entry.size > 2147483648) return;
    const url = api.streamUrl(target.rootId, target.path ? `${target.path}/${target.entry.name}` : target.entry.name);
    let dead = false;
    (async () => {
      try {
        const res = await fetch(url, { credentials: "same-origin" });
        if (!res.ok || dead) return;
        if (isImg) {
          const blob = await res.blob();
          if (!dead) setPreview({ kind: "image", url: URL.createObjectURL(blob) });
        } else if (isPdf) {
          const blob = await res.blob();
          const pdfBlob = new Blob([blob], { type: "application/pdf" });
          if (!dead) setPreview({ kind: "pdf", url: URL.createObjectURL(pdfBlob) });
        } else if (isVideo || isAudio) {
          if (!dead) setPreview({ kind: isVideo ? "video" : "audio", url });
        } else {
          const text = await res.text();
          if (!dead) setPreview({ kind: "text", text });
        }
      } catch {
        /* 预览失败静默（不影响元数据展示） */
      }
    })();
    return () => {
      dead = true;
      setPreview((p) => {
        if (p.url) URL.revokeObjectURL(p.url);
        return { kind: null };
      });
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target?.rootId, target?.path, target?.entry.name, err]);

  if (!target) return null;
  const ic = iconClassForName(target.entry.name, target.entry.kind);
  const fullPath =
    target.path ? `${target.rootId.split(":")[1]}/${target.path}/${target.entry.name}` : `${target.rootId.split(":")[1]}/${target.entry.name}`;

  const copyPath = async () => {
    try {
      await navigator.clipboard.writeText(detail?.fullPath ?? fullPath);
      toast(t("msg.copied"));
    } catch {
      toast(detail?.fullPath ?? fullPath, "warn");
    }
  };

  const startHash = () => {
    if (!target) return;
    const rootRef = target.rootId;
    api
      .plan({
        action: "hash",
        source: { root: rootFromId(rootRef), path: target.path ? `${target.path}/${target.entry.name}` : target.entry.name },
        clientRequestId: undefined,
      })
      .then(async (plan) => {
        if (!plan.token) throw new TroveError("PLAN", "no token", false, 0);
        const job = await api.submitJob(plan.id, plan.token);
        setHash({ state: "running", jobId: job.id });
        const poll = setInterval(async () => {
          try {
            const j = await api.job(job.id);
            if (j.state === "completed" && j.hashes?.length) {
              clearInterval(poll);
              setHash({ state: "done", value: j.hashes[0].sha256 });
            } else if (j.state === "failed" || j.state === "failed_retryable" || j.state === "cancelled") {
              clearInterval(poll);
              setHash({ state: "error" });
            }
          } catch {
            clearInterval(poll);
            setHash({ state: "error" });
          }
        }, 1200);
      })
      .catch((e: unknown) => {
        toast(e instanceof TroveError ? errText(e.code) : String(e), "err");
        setHash({ state: "error" });
      });
  };

  return (
    <div class="tv-inspector" role="complementary">
      <div class="head">
        {t("ins.title")}
        <span class="tv-spacer" />
        <button class="tv-btn ghost" onClick={onClose} aria-label="close">
          <IconClose size={14} />
        </button>
      </div>
      <div class="body">
        <div class={"tv-ic iconbig " + ic}>
          {IconFor(ic === "dir" ? "dir" : ic, { size: 30 })}
        </div>
        <div class="fname">{target.entry.name}</div>
        <div class="ftype">{t("kind." + target.entry.kind)}</div>

        {preview.kind === "image" && (
          <img class="tv-preview" src={preview.url} alt="" style="max-width:100%;border-radius:8px;margin-top:8px" />
        )}
        {preview.kind === "pdf" && (
          <embed class="tv-preview" src={preview.url} type="application/pdf" style="width:100%;height:240px;border-radius:8px;margin-top:8px" />
        )}
        {preview.kind === "video" && (
          <video class="tv-preview" src={preview.url} controls style="width:100%;border-radius:8px;margin-top:8px" />
        )}
        {preview.kind === "audio" && (
          <audio src={preview.url} controls style="width:100%;margin-top:8px" />
        )}
        {preview.kind === "text" && (
          <pre class="tv-hashout" style="max-height:200px;overflow:auto;margin-top:8px">{preview.text}</pre>
        )}

        {err ? (
          <div class="tv-alert warn" style="margin-top:10px">
            <IconTrash size={13} />
            <span class="txt">{errText(err)}</span>
          </div>
        ) : !detail ? (
          <div style="margin-top:14px">
            <div class="tv-spinner" />
          </div>
        ) : (
          <>
            <h4>
              <IconFolder size={13} />
              {t("ins.basic")}
            </h4>
            <div class="tv-kv">
              <span class="k">{t("ins.size")}</span>
              <span class="tv-num">
                {detail.kind === "dir" ? "—" : `${formatBytes(detail.size)} (${detail.size} B)`}
              </span>
              <span class="k">{t("ins.mtime")}</span>
              <span>{formatTime(detail.mtime)}</span>
              {detail.target && (
                <>
                  <span class="k">→</span>
                  <span class="tv-dim" style="word-break:break-all">{detail.target}</span>
                </>
              )}
            </div>

            <h4>
              <IconDisk size={13} />
              {t("ins.storage")}
            </h4>
            <div class="tv-kv">
              <span class="k">{storageKindLabel(detail.root.kind)}</span>
              <span>{detail.root.id}</span>
              <span class="k">{t("ins.fullPath")}</span>
              <span class="tv-dim mono" style="word-break:break-all;font-family:var(--tv-mono);font-size:11.5px">
                {detail.fullPath}
              </span>
            </div>

            <h4>
              <IconSSD size={13} />
              {t("ins.perm")}
            </h4>
            <div class="tv-kv">
              <span class="k">{t("ins.mode")}</span>
              <span class="mono" style="font-family:var(--tv-mono)">{detail.mode}</span>
              <span class="k">{t("ins.owner")}</span>
              <span>
                {detail.uid}:{detail.gid}
              </span>
              {detail.protected && (
                <>
                  <span class="k">⚠</span>
                  <span style="color:var(--tv-amber)">{t("nav.protected")}</span>
                </>
              )}
            </div>

            <h4>
              <IconHash size={13} />
              {t("ins.hash")}
            </h4>
            {hash.state === "idle" && (
              <button class="tv-btn" onClick={startHash}>
                <IconHash size={13} />
                {t("act.hash")}
              </button>
            )}
            {hash.state === "running" && (
              <div style="display:flex;gap:8px;align-items:center">
                <div class="tv-spinner" style="width:16px;height:16px;border-width:2px" />
                {t("task.runningState")}
              </div>
            )}
            {hash.state === "done" && (
              <div>
                <div class="tv-kv">
                  <span class="k">{t("ins.sha256")}</span>
                  <span class="done" style="display:flex;gap:6px;align-items:center;color:var(--tv-green)">
                    <IconCheck size={12} />
                  </span>
                </div>
                <div class="tv-hashout">{hash.value}</div>
              </div>
            )}
            {hash.state === "error" && <span class="tv-dim">—</span>}

            <h4>
              ⚡
              {t("ins.actions")}
            </h4>
            <div class="actions">
              <button class="tv-btn" onClick={() => void copyPath()}>
                <IconCopy size={13} />
                {t("ins.copyPath")}
              </button>
              <button class="tv-btn" onClick={() => onMove(target.entry)}>
                <IconMove size={13} />
                {t("act.moveToPane")}
              </button>
              <button class="tv-btn" onClick={() => onRename(target.entry)}>
                <IconPencil size={13} />
                {t("act.rename")}
              </button>
              {detail.kind === "file" && (
                <a
                  class="tv-btn"
                  href={api.downloadUrl(detail.root.kind + ":" + detail.root.id, detail.path)}
                >
                  <IconCopy size={13} />
                  {t("act.download")}
                </a>
              )}
              <button class="tv-btn" onClick={() => onShare?.(target)}>
                <IconLink size={13} />
                {t("share.title")}
              </button>
              <button class="tv-btn danger" onClick={() => onDelete(target.entry)}>
                <IconTrash size={13} />
                {t("act.delete")}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}

function rootFromId(rootId: string): { kind: "share" | "disk" | "pool"; id: string } {
  const [kind, id] = rootId.split(":");
  return { kind: kind as "share" | "disk" | "pool", id };
}

function storageKindLabel(kind: string): string {
  switch (kind) {
    case "share":
      return t("ins.share");
    case "disk":
      return t("ins.disk");
    default:
      return t("ins.pool");
  }
}
