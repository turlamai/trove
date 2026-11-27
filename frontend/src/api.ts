// Typed client for the Trove daemon API (via the same-origin PHP gateway).
// All requests carry the WebGUI CSRF token on writes; errors surface as
// TroveError with the daemon's stable codes.

export type RootKind = "share" | "disk" | "pool";

export interface RootRef {
  kind: RootKind;
  id: string;
}

export interface RootInfo {
  kind: RootKind;
  id: string;
  name: string;
  path: string;
  fsType?: string;
  readOnly: boolean;
  protected: boolean;
  expertOnly: boolean;
}

export type EntryKind = "dir" | "file" | "symlink" | "other";

export interface Entry {
  name: string;
  kind: EntryKind;
  size: number; // -1 = unknown
  mode: string;
  mtime: string;
  target?: string;
  dev: number;
  ino: number;
}

export interface Listing {
  root: RootRef;
  path: string;
  total: number;
  offset: number;
  entries: Entry[];
}

export interface Conflict {
  path: string;
  srcSize: number;
  srcMtime: string;
  dstSize: number;
  dstMtime: string;
  resolved?: string;
}

export interface Risk {
  code: string;
  note: string;
}

export interface Plan {
  id: string;
  action: string;
  source?: Node;
  targetDir?: Node;
  items: number;
  files: number;
  dirs: number;
  bytes: number;
  estimate: boolean;
  conflicts?: Conflict[];
  risks?: Risk[];
  needsAck?: boolean;
  token?: string;
  expiresAt: string;
  namespaceOk: boolean;
}

export interface Node {
  root: RootRef;
  path: string;
}

export interface Job {
  id: string;
  clientRequestId?: string;
  action: string;
  state: string;
  createdAt: string;
  startedAt?: string;
  endedAt?: string;
  plan?: Plan;
  totalItems: number;
  doneItems: number;
  totalBytes: number;
  doneBytes: number;
  currentFile?: string;
  speedBps?: number;
  etaSeconds?: number;
  errorCode?: string;
  message?: string;
  warnings?: string[];
  conflicts?: Conflict[];
  hashes?: { rel: string; size: number; sha256: string }[];
  cancelable: boolean;
  pausable: boolean;
}

export interface Capabilities {
  version: string;
  apiVersion: string;
  readOk: boolean;
  writeOk: boolean;
  readOnlyWhy?: string;
  dataDir?: string;
  dataDirOk: boolean;
  expertMode: boolean;
  physicalWrite: boolean;
  concurrency: number;
  openat2: boolean;
  arrayState?: string;
  actions: string[];
}

export interface Settings {
  dataDir: string;
  concurrency: number;
  physicalWrite: boolean;
  protectedMode: string;
  trash: boolean;
  shareEnable: boolean;
  sharePort: number;
  shareBind: string;
  expertMode: boolean;
  showHidden: boolean;
  language: string;
}

export interface EntryDetail {
  root: RootRef;
  path: string;
  name: string;
  kind: EntryKind;
  size: number;
  mode: string;
  mtime: string;
  uid: number;
  gid: number;
  nlink: number;
  target?: string;
  fullPath: string;
  protected: boolean;
}

export interface ShareLink {
  token: string;
  root: { kind: RootKind; id: string };
  relPath: string;
  name: string;
  createdAt: string;
  expiresAt?: string;
  passwordHash?: string;
  downloads: number;
  lastDownload?: string;
  note?: string;
}

function parseRootId(rootId: string): { kind: RootKind; id: string } {
  const [kind, id] = rootId.split(":");
  return { kind: kind as RootKind, id };
}

export class TroveError extends Error {
  code: string;
  recoverable: boolean;
  status: number;
  constructor(code: string, message: string, recoverable: boolean, status: number) {
    super(message);
    this.code = code;
    this.recoverable = recoverable;
    this.status = status;
  }
}

const CSRF_HEADER = "X-Trove-Csrf";
const TOKEN_HEADER = "X-Trove-Token";

let boot = {
  api: "/plugins/trove/gateway.php",
  csrf: "",
  unraidToken: "",
  token: "",
  version: "dev",
};

export function initApi(b: Partial<typeof boot>) {
  boot = { ...boot, ...b };
}

async function refreshCsrf(apiUrl: string): Promise<boolean> {
  try {
    const t = await fetch(apiUrl + "?cmd=csrf", { credentials: "same-origin" });
    if (!t.ok) return false;
    const fresh = await t.json();
    if (fresh?.csrf) {
      boot.csrf = fresh.csrf;
      return true;
    }
  } catch {
    /* ignore */
  }
  return false;
}

async function request<T>(
  method: string,
  path: string,
  opts: {
    body?: unknown;
    query?: Record<string, string | number | undefined>;
    replayed?: boolean;
  } = {},
): Promise<T> {
  const url = new URL(boot.api, window.location.origin);
  url.searchParams.set("cmd", path);
  for (const [k, v] of Object.entries(opts.query ?? {})) {
    if (v !== undefined) url.searchParams.set(k, String(v));
  }
  const headers: Record<string, string> = {
    "X-Requested-With": "trove",
  };
  if (boot.token) headers[TOKEN_HEADER] = boot.token;
  // Writes POST a form-encoded body: Unraid's nginx CSRF layer only
  // inspects `csrf_token` form fields, and the gateway reads the JSON
  // payload from the `payload` field.
  let body: string | undefined;
  if (method !== "GET") {
    headers[CSRF_HEADER] = boot.csrf;
    // Unraid 7 nginx validates the `token` field (unraid_token) on POSTs;
    // the gateway additionally accepts csrf_token/unraid_token values.
    body = new URLSearchParams({
      csrf_token: boot.csrf,
      token: boot.unraidToken,
      payload: JSON.stringify(opts.body ?? {}),
    }).toString();
    headers["Content-Type"] = "application/x-www-form-urlencoded;charset=UTF-8";
  }
  let res: Response;
  try {
    res = await fetch(url.toString(), {
      method,
      headers,
      body,
      credentials: "same-origin",
    });
  } catch {
    throw new TroveError("NETWORK", "network error", true, 0);
  }
  const ct = res.headers.get("Content-Type") ?? "";
  // Unraid's nginx CSRF layer silently drops POSTs whose csrf_token field
  // no longer matches the rotated var.ini token: the client sees an empty
  // non-JSON 200. Refresh the token once and replay the request.
  const looksDropped = method !== "GET" && res.status === 200 && !ct.includes("json");
  if (looksDropped && !opts.replayed) {
    const refreshed = await refreshCsrf(url.toString().split("?")[0]);
    if (refreshed) {
      return request<T>(method, path, { ...opts, replayed: true });
    }
    throw new TroveError("CSRF_INVALID", "security token expired; page reload required", true, res.status);
  }
  if (!ct.includes("json")) {
    // Non-JSON (e.g. gateway error page) — never parse as data.
    throw new TroveError("GATEWAY", `unexpected gateway response (${res.status})`, true, res.status);
  }
  const data = await res.json();
  if (!res.ok && data && data.error) {
    // var.ini rotates its CSRF token on emhttp events; a stale page token
    // surfaces as CSRF_INVALID — refresh once and replay the request.
    if (data.error.code === "CSRF_INVALID" && !opts.replayed) {
      const refreshed = await refreshCsrf(url.toString().split("?")[0]);
      if (refreshed) {
        return request<T>(method, path, { ...opts, replayed: true });
      }
    }
    throw new TroveError(data.error.code, data.error.message, !!data.error.recoverable, res.status);
  }
  if (!res.ok) {
    throw new TroveError("GATEWAY", `HTTP ${res.status}`, true, res.status);
  }
  return data as T;
}

export const api = {
  capabilities: () => request<Capabilities>("GET", "capabilities"),
  roots: () => request<{ roots: RootInfo[] }>("GET", "roots"),
  entries: (q: {
    rootId: string;
    path?: string;
    sort?: string;
    dir?: string;
    offset?: number;
    limit?: number;
    filter?: string;
  }) => request<Listing>("GET", "entries", { query: q }),
  entry: (rootId: string, path: string) =>
    request<EntryDetail>("GET", "entry", { query: { rootId, path } }),
  plan: (req: {
    action: string;
    source?: Node;
    targets?: Node[];
    targetDir?: Node;
    targetName?: string;
    conflictPolicy?: string;
    sourcePolicy?: string;
    verify?: string;
    ackProtected?: boolean;
    purge?: boolean;
    clientRequestId?: string;
  }) => request<Plan>("POST", "operations/plan", { body: req }),
  execute: (planId: string, token: string, decision?: string) =>
    request<{ status: string }>("POST", "operations/execute", {
      body: { planId, token, decision: decision ?? "" },
    }),
  submitJob: (planId: string, token: string | undefined, clientRequestId?: string) =>
    request<Job>("POST", "job_submit", { body: { planId, token, clientRequestId } }),
  jobs: (limit = 50) => request<{ jobs: Job[] }>("GET", "jobs", { query: { limit } }),
  job: (id: string) => request<Job>("GET", "jobs/" + encodeURIComponent(id)),
  cancelJob: (id: string) =>
    request<{ status: string }>("POST", `jobs/${encodeURIComponent(id)}/cancel`, { body: {} }),
  retryJob: (id: string) => request<Job>("POST", `jobs/${encodeURIComponent(id)}/retry`, { body: {} }),
  resolveJob: (id: string, decisions: { path: string; resolution: string }[]) =>
    request<{ status: string }>("POST", `jobs/${encodeURIComponent(id)}/resolve`, {
      body: { decisions },
    }),
  jobEvents: (id: string, since = 0) =>
    request<{ events: { seq: number; type: string; text: string; at: string }[] }>(
      "GET",
      `jobs/${encodeURIComponent(id)}/events`,
      { query: { since } },
    ),
  uploadUrl: (rootId: string, dir: string, name: string, policy: string) => {
    const url = new URL(boot.api, window.location.origin);
    url.searchParams.set("cmd", "upload");
    url.searchParams.set("rootId", rootId);
    url.searchParams.set("dir", dir);
    url.searchParams.set("name", name);
    url.searchParams.set("policy", policy);
    return url.toString();
  },
  trashList: (rootId: string) =>
    request<{ entries: { name: string; originRel: string; kind: string; size: number; deletedAt: string }[] }>(
      "GET", "trash_list", { query: { rootId } }),
  trashRestore: (rootId: string, name: string) =>
    request<{ status: string; restored: string }>("POST", "trash_restore", {
      body: { root: parseRootId(rootId), name },
    }),
  sharesList: () => request<{ shares: ShareLink[] }>("GET", "shares"),
  sharesCreate: (opts: { rootId: string; path: string; expiryDays: number; password: string; note?: string }) =>
    request<ShareLink>("POST", "shares_create", { body: { ...opts } }),
  sharesRevoke: (token: string) =>
    request<{ status: string }>("POST", "shares_revoke", { body: { token } }),
  zipUrl: (rootId: string, path: string) => {
    const url = new URL(boot.api, window.location.origin);
    url.searchParams.set("cmd", "zip");
    url.searchParams.set("rootId", rootId);
    url.searchParams.set("path", path);
    return url.toString();
  },
  streamUrl: (rootId: string, path: string) => {
    const url = new URL(boot.api, window.location.origin);
    url.searchParams.set("cmd", "stream");
    url.searchParams.set("rootId", rootId);
    url.searchParams.set("path", path);
    return url.toString();
  },
  settings: () => request<Settings>("GET", "settings"),
  // POST from the browser (nginx CSRF layer); the gateway forwards as PUT.
  saveSettings: (patch: Partial<Settings>) => request<{ status: string }>("POST", "settings_put", { body: patch }),
  search: (rootId: string, path: string, q: string, limit = 500) =>
    request<{
      root: RootRef;
      query: string;
      scanned: number;
      truncated: boolean;
      hits: { path: string; kind: string; size: number; mtime: string }[];
    }>("GET", "search", { query: { rootId, path, q, limit } }),
  uploadFile: (opts: {
    rootId: string;
    dir: string;
    name: string;
    policy?: string;
    file: File;
    onProgress?: (loaded: number, total: number) => void;
  }): Promise<{ status: string; name: string; size: number }> => {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const url = new URL(boot.api, window.location.origin);
      url.searchParams.set("cmd", "upload");
      url.searchParams.set("rootId", opts.rootId);
      url.searchParams.set("dir", opts.dir);
      url.searchParams.set("name", opts.name);
      url.searchParams.set("policy", opts.policy ?? "rename");
      xhr.open("PUT", url.toString());
      xhr.setRequestHeader("X-Trove-Csrf", boot.csrf);
      xhr.upload.onprogress = (ev) => {
        opts.onProgress?.(ev.loaded, ev.total);
      };
      xhr.onload = () => {
        const ct = xhr.getResponseHeader("Content-Type") ?? "";
        if (!ct.includes("json")) {
          reject(new TroveError("CSRF_INVALID", "security token expired; page reload required", true, xhr.status));
          return;
        }
        try {
          const data = JSON.parse(xhr.responseText);
          if (xhr.status >= 200 && xhr.status < 300 && data.status === "ok") {
            resolve(data);
          } else if (data.error) {
            reject(new TroveError(data.error.code, data.error.message, !!data.error.recoverable, xhr.status));
          } else {
            reject(new TroveError("GATEWAY", "unexpected upload response", true, xhr.status));
          }
        } catch {
          reject(new TroveError("GATEWAY", "unexpected upload response", true, xhr.status));
        }
      };
      xhr.onerror = () => reject(new TroveError("NETWORK", "network error during upload", true, 0));
      xhr.send(opts.file);
    });
  },
  downloadUrl: (rootId: string, path: string) => {
    const url = new URL(boot.api, window.location.origin);
    url.searchParams.set("cmd", "download");
    url.searchParams.set("rootId", rootId);
    url.searchParams.set("path", path);
    return url.toString();
  },
};

// formatBytes mirrors the mockup style: 55.68 GB, 217 KB, —
export function formatBytes(n: number): string {
  if (n < 0) return "—";
  if (n === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 ? Math.round(v) : v.toFixed(2).replace(/\.?0+$/, "")} ${units[i]}`;
}

export function formatTime(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
