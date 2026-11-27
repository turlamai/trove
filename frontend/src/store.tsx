// Global app state via context + hooks (no external state library).
import { createContext } from "preact";
import { useContext, useEffect, useRef, useState } from "preact/hooks";
import { api, Capabilities, Job, RootInfo, TroveError } from "./api";
import { errText, setLang, Lang, t } from "./i18n";

export interface Toast {
  id: number;
  kind: "ok" | "err" | "warn";
  text: string;
}

export interface UploadTask {
  id: number;
  name: string;
  dir: string;
  rootId: string;
  loaded: number;
  total: number;
  state: "uploading" | "done" | "error";
  error?: string;
  _file?: File;
}

export interface Clipboard {
  mode: "copy" | "cut";
  rootId: string;
  paths: string[];
}

export interface UIPrefs {
  showHidden: boolean;
  dualDefault: boolean;
  lang: Lang;
}

function loadPrefs(): UIPrefs {
  try {
    const raw = localStorage.getItem("trove.prefs");
    if (raw) return { showHidden: false, dualDefault: false, lang: "zh-CN", ...JSON.parse(raw) };
  } catch {
    /* ignore */
  }
  const nav = (navigator.language || "zh-CN").startsWith("en") ? "en" : "zh-CN";
  return { showHidden: false, dualDefault: false, lang: nav as Lang };
}

function savePrefs(p: UIPrefs) {
  try {
    localStorage.setItem("trove.prefs", JSON.stringify(p));
  } catch {
    /* ignore */
  }
}

interface AppState {
  caps: Capabilities | null;
  roots: RootInfo[];
  shares: RootInfo[];
  devices: RootInfo[];
  prefs: UIPrefs;
  setPrefs: (p: Partial<UIPrefs>) => void;
  jobs: Job[];
  refreshJobs: () => void;
  toasts: Toast[];
  toast: (text: string, kind?: Toast["kind"]) => void;
  toastErr: (e: unknown) => void;
  clipboard: Clipboard | null;
  setClipboard: (c: Clipboard | null) => void;
  reload: () => void;
  genId: () => string;
  uploads: UploadTask[];
  queueUploads: (rootId: string, dir: string, files: File[]) => void;
  clearUploads: () => void;
}

const Ctx = createContext<AppState>(null as unknown as AppState);

export function useApp(): AppState {
  return useContext(Ctx);
}

let toastSeq = 1;

export function AppProvider({ children }: { children: preact.ComponentChildren }) {
  const [caps, setCaps] = useState<Capabilities | null>(null);
  const [roots, setRoots] = useState<RootInfo[]>([]);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [clipboard, setClipboardState] = useState<Clipboard | null>(null);
  const [prefsState, setPrefsState] = useState<UIPrefs>(loadPrefs);
  const [tick, setTick] = useState(0);
  const [uploads, setUploadsState] = useState<UploadTask[]>([]);
  const jobsActive = useRef(false);
  const uploadSeq = useRef(1);

  const prefs = prefsState;
  setLang(prefs.lang);

  const reload = () => setTick((x) => x + 1);
  useEffect(() => {
    (async () => {
      try {
        const [c, r] = await Promise.all([api.capabilities(), api.roots()]);
        setCaps(c);
        setRoots(r.roots);
        if (!c.writeOk) {
          // visible, non-blocking hint handled by consumers
        }
      } catch (e) {
        setCaps(null);
        toastErrGlobal(e, setToasts);
      }
    })();
  }, [tick]);

  const refreshJobs = () => {
    api
      .jobs(100)
      .then((r) => {
        setJobs(r.jobs);
        jobsActive.current = r.jobs.some(
          (j) => j.state === "running" || j.state === "queued" || j.state === "verifying",
        );
      })
      .catch(() => {});
  };

  // Job polling: fast while something runs, slow otherwise.
  useEffect(() => {
    refreshJobs();
    let stop = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = () => {
      if (stop) return;
      api
        .jobs(100)
        .then((r) => {
          setJobs(r.jobs);
          const active = r.jobs.some(
            (j) =>
              j.state === "running" || j.state === "queued" || j.state === "verifying" ||
              j.state === "cancel_requested",
          );
          timer = setTimeout(poll, active ? 1200 : 6000);
        })
        .catch(() => {
          timer = setTimeout(poll, 5000);
        });
    };
    timer = setTimeout(poll, 1200);
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, []);

  const setPrefs = (p: Partial<UIPrefs>) => {
    const next = { ...prefsState, ...p };
    setPrefsState(next);
    savePrefs(next);
    if (p.lang) setLang(p.lang);
  };

  const toast = (text: string, kind: Toast["kind"] = "ok") => {
    const id = toastSeq++;
    setToasts((ts) => [...ts, { id, kind, text }]);
    setTimeout(() => setToasts((ts) => ts.filter((x) => x.id !== id)), 4200);
  };

  const toastErr = (e: unknown) => toastErrGlobal(e, setToasts);

  const genId = () =>
    (crypto?.randomUUID?.() ??
      `req-${Date.now()}-${Math.random().toString(36).slice(2)}`) as string;

  const queueUploads = (rootId: string, dir: string, files: File[]) => {
    const batch = files.map((f) => ({
      id: uploadSeq.current++,
      name: f.name,
      dir,
      rootId,
      loaded: 0,
      total: f.size,
      state: "uploading" as const,
      _file: f,
    }));
    setUploadsState((u) => [...u, ...batch]);
    // 逐个顺序上传（并发 1，避免占满上行）
    (async () => {
      for (const t of batch) {
        try {
          await api.uploadFile({
            rootId,
            dir,
            name: t.name,
            policy: "rename",
            file: t._file,
            onProgress: (loaded, total) => {
              setUploadsState((u) => u.map((x) => (x.id === t.id ? { ...x, loaded, total } : x)));
            },
          });
          setUploadsState((u) => u.map((x) => (x.id === t.id ? { ...x, state: "done" } : x)));
        } catch (e) {
          setUploadsState((u) =>
            u.map((x) => (x.id === t.id ? { ...x, state: "error", error: String(e) } : x)));
        }
      }
      setTimeout(() => setUploadsState((u) => u.filter((x) => x.state === "uploading")), 6000);
    })();
  };

  const clearUploads = () => setUploadsState([]);

  const shares = roots.filter((r) => r.kind === "share");
  const devices = roots.filter((r) => r.kind !== "share");

  return (
    <Ctx.Provider
      value={{
        caps,
        roots,
        shares,
        devices: devices ?? [],
        prefs,
        setPrefs,
        jobs,
        refreshJobs,
        toasts,
        toast,
        toastErr,
        clipboard,
        setClipboard: (c) => {
          setClipboardState(c);
          if (c) {
            try {
              sessionStorage.setItem("trove.clip", JSON.stringify(c));
            } catch {
              /* ignore */
            }
          } else {
            sessionStorage.removeItem("trove.clip");
          }
        },
        reload,
        genId,
        uploads,
        queueUploads,
        clearUploads,
      }}
    >
      {children}
    </Ctx.Provider>
  );
}

function toastErrGlobal(e: unknown, setToasts: (f: (ts: Toast[]) => Toast[]) => void) {
  const msg = e instanceof TroveError ? errText(e.code) : String(e);
  const id = toastSeq++;
  setToasts((ts) => [...ts, { id, kind: "err", text: msg }]);
  setTimeout(() => setToasts((ts) => ts.filter((x) => x.id !== id)), 5200);
}

export { t };
