// File pane: toolbar, breadcrumb, virtualized table, status bar.
import { Fragment } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import {
  Entry,
  Listing,
  RootRef,
  api,
  formatBytes,
  formatTime,
  TroveError,
} from "./api";
import { iconClassForName, IconFor, IconRefresh, IconSearch } from "./icons";
import { errText, t } from "./i18n";
import { useApp } from "./store";

export const PAGE = 500;
export const ROW_H = 31;

export interface PaneState {
  root: RootRef | null;
  path: string;
  entries: Entry[];
  total: number;
  loading: boolean;
  errCode: string | null;
  sort: "name" | "size" | "mtime" | "kind";
  dir: "asc" | "desc";
  filter: string;
  selection: Set<string>;
  cursor: number;
  history: { root: RootRef; path: string }[];
  histIdx: number;
  searchHits: { path: string; kind: string; size: number; mtime: string }[] | null;
}

export function newPane(root: RootRef | null = null): PaneState {
  return {
    root,
    path: "",
    entries: [],
    total: 0,
    loading: false,
    errCode: null,
    sort: "name",
    dir: "asc",
    filter: "",
    selection: new Set(),
    cursor: -1,
    history: root ? [{ root, path: "" }] : [],
    histIdx: root ? 0 : -1,
    searchHits: null,
  };
}

interface PaneProps {
  state: PaneState;
  focused: boolean;
  onFocus: () => void;
  onChange: (patch: Partial<PaneState>, opts?: { pushHistory?: boolean }) => void;
  onOpenEntry: (e: Entry) => void;
  onContext: (ev: MouseEvent, entry: Entry | null) => void;
  onSelectionEnd: () => void;
  onInspect: (e: Entry | null) => void;
  onDragStartEntries: (paths: string[]) => void;
  dropActive: boolean;
  onDrop: () => void;
  dragIn: boolean;
}

export function FilePane(props: PaneProps) {
  const { state: s, onChange } = props;
  const { prefs } = useApp();
  const [dragOver, setDragOver] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [range, setRange] = useState({ start: 0, end: 60 });
  const loadSeq = useRef(0);

  const visible = useMemo(
    () => (prefs.showHidden ? s.entries : s.entries.filter((e) => !e.name.startsWith("."))),
    [s.entries, prefs.showHidden],
  );

  // (re)load listing when navigation/sort changes
  useEffect(() => {
    if (!s.root) return;
    const seq = ++loadSeq.current;
    onChange({ loading: true, errCode: null });
    api
      .entries({
        rootId: `${s.root.kind}:${s.root.id}`,
        path: s.path,
        sort: s.sort,
        dir: s.dir,
        offset: 0,
        limit: PAGE,
        filter: s.filter || undefined,
      })
      .then((l: Listing) => {
        if (seq !== loadSeq.current) return;
        onChange({ entries: l.entries, total: l.total, loading: false, cursor: -1, selection: new Set() });
      })
      .catch((e: unknown) => {
        if (seq !== loadSeq.current) return;
        const code = e instanceof TroveError ? e.code : "IO_FAILURE";
        onChange({ entries: [], total: 0, loading: false, errCode: code });
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [s.root?.kind, s.root?.id, s.path, s.sort, s.dir, s.filter]);

  // progressive fetch on scroll
  const maybeFetchMore = () => {
    const el = wrapRef.current;
    if (!el || !s.root) return;
    if (s.entries.length >= s.total) return;
    if (el.scrollTop + el.clientHeight < el.scrollHeight - 400) return;
    const seq = loadSeq.current;
    api
      .entries({
        rootId: `${s.root.kind}:${s.root.id}`,
        path: s.path,
        sort: s.sort,
        dir: s.dir,
        offset: s.entries.length,
        limit: PAGE,
        filter: s.filter || undefined,
      })
      .then((l: Listing) => {
        if (seq !== loadSeq.current) return;
        onChange({ entries: [...s.entries, ...l.entries], total: l.total });
      })
      .catch(() => {});
  };

  // virtualization window
  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const update = () => {
      const first = Math.max(0, Math.floor(el.scrollTop / ROW_H) - 10);
      const count = Math.ceil(el.clientHeight / ROW_H) + 20;
      setRange({ start: first, end: first + count });
    };
    update();
    el.addEventListener("scroll", update, { passive: true });
    return () => el.removeEventListener("scroll", update);
  }, [s.root?.id, s.path, s.filter]);

  const slice = visible.slice(range.start, range.end);

  const clickRow = (ev: MouseEvent, idx: number) => {
    const entry = visible[idx];
    if (ev.shiftKey && s.cursor >= 0) {
      const [a, b] = [Math.min(s.cursor, idx), Math.max(s.cursor, idx)];
      const sel = new Set<string>();
      for (let i = a; i <= b; i++) sel.add(visible[i].name);
      onChange({ selection: sel, cursor: idx });
    } else if (ev.ctrlKey || ev.metaKey) {
      const sel = new Set(s.selection);
      if (sel.has(entry.name)) sel.delete(entry.name);
      else sel.add(entry.name);
      onChange({ selection: sel, cursor: idx });
    } else {
      onChange({ selection: new Set([entry.name]), cursor: idx });
    }
    props.onSelectionEnd();
    props.onInspect(entry);
  };

  const keyNav = (ev: KeyboardEvent) => {
    if (!props.focused) return;
    const n = visible.length;
    if (!n) return;
    let idx = s.cursor;
    switch (ev.key) {
      case "ArrowDown":
        idx = Math.min(n - 1, s.cursor < 0 ? 0 : s.cursor + 1);
        break;
      case "ArrowUp":
        idx = Math.max(0, s.cursor < 0 ? 0 : s.cursor - 1);
        break;
      case "Home":
        idx = 0;
        break;
      case "End":
        idx = n - 1;
        break;
      default:
        return;
    }
    ev.preventDefault();
    onChange({ cursor: idx, selection: new Set([visible[idx].name]) });
    const el = wrapRef.current;
    if (el) {
      const y = idx * ROW_H;
      if (y < el.scrollTop) el.scrollTop = y;
      if (y + ROW_H > el.scrollTop + el.clientHeight) el.scrollTop = y - el.clientHeight + ROW_H;
    }
    props.onInspect(visible[idx]);
  };

  const crumbs = buildCrumbs(s.root, s.path);
  const selCount = s.selection.size;
  const selBytes = visible.reduce((acc, e) => (s.selection.has(e.name) ? acc + e.size : acc), 0);

  return (
    <div
      class={"tv-pane" + (props.focused ? " focused" : "") + (dragOver ? " tv-drop-target" : "")}
      onMouseDown={props.onFocus}
      onDragOver={(ev: DragEvent) => {
        if (props.dragIn) {
          ev.preventDefault();
          if (ev.dataTransfer) ev.dataTransfer.dropEffect = "copy";
          setDragOver(true);
        }
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={(ev: DragEvent) => {
        setDragOver(false);
        if (props.dragIn) {
          ev.preventDefault();
          props.onDrop();
        }
      }}
    >
      {/* toolbar */}
      <div class="tv-tools">
        <div class="tv-crumb">
          {crumbs.length === 0 && <span class="tv-dim">{t("st.noRoots")}</span>}
          {crumbs.map((c, i) => (
            <Fragment key={i}>
              {i > 0 && <span class="tv-crumbsep">/</span>}
              <button
                class="tv-crumblink"
                onClick={() => onChange({ path: c.path }, { pushHistory: true })}
                title={c.path || t("bc.root")}
              >
                {c.home ? "⌂" : c.label}
              </button>
            </Fragment>
          ))}
        </div>
        <div class="tv-filterbox">
          <IconSearch size={14} />
          <input
            placeholder={t("tb.filter")}
            value={s.filter}
            onInput={(ev: InputEvent) =>
              onChange({ filter: (ev.target as HTMLInputElement).value })
            }
          />
        </div>
        <button class="tv-btn ghost" title={t("tb.refresh") + " (F5)"} onClick={() => onChange({})}>
          <IconRefresh size={14} />
        </button>
      </div>

      {/* content */}
      {s.loading && s.entries.length === 0 ? (
        <div class="tv-state">
          <div class="tv-spinner" />
          {t("st.loading")}
        </div>
      ) : s.errCode ? (
        <div class="tv-state err">
          {IconFor("file", { size: 36 })}
          <div>{errText(s.errCode) !== s.errCode ? errText(s.errCode) : t("st.error")}</div>
          <button class="tv-btn" onClick={() => onChange({})}>
            {t("tb.refresh")}
          </button>
        </div>
      ) : s.searchHits !== null ? (
        <SearchResultsTable
          hits={s.searchHits}
          onOpen={(p) => {
            const parent = p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "";
            onChange({ path: parent, searchHits: null, filter: "" }, { pushHistory: true });
          }}
        />
      ) : visible.length === 0 ? (
        <div class="tv-state">
          {IconFor("dir", { size: 36 })}
          <div>{s.filter ? "— 0 —" : t("st.empty")}</div>
        </div>
      ) : (
        <div
          class="tv-tablewrap"
          ref={wrapRef}
          tabIndex={0}
          onKeyDown={keyNav}
          onScroll={maybeFetchMore}
          onContextMenu={(ev: MouseEvent) => {
            const target = ev.target as HTMLElement;
            if (target.closest("tr.row")) return; // row handler deals with it
            ev.preventDefault();
            onChange({ selection: new Set() });
            props.onContext(ev, null);
          }}
        >
          <table class="tv-table">
            <thead>
              <tr>
                <th style="width:34px">
                  <input
                    type="checkbox"
                    class="tv-checkbox"
                    checked={selCount > 0 && selCount === visible.length}
                    onClick={(ev: MouseEvent) => {
                      ev.stopPropagation();
                      const all = selCount === visible.length;
                      const sel = new Set<string>();
                      if (!all) visible.forEach((e) => sel.add(e.name));
                      onChange({ selection: sel });
                    }}
                  />
                </th>
                <th
                  class="sortable"
                  onClick={() => onChange({ sort: "name", dir: s.sort === "name" && s.dir === "asc" ? "desc" : "asc" })}
                >
                  {t("col.name")} {s.sort === "name" ? (s.dir === "asc" ? "↑" : "↓") : ""}
                </th>
                <th
                  class="sortable"
                  style="width:110px"
                  onClick={() => onChange({ sort: "size", dir: s.sort === "size" && s.dir === "asc" ? "desc" : "asc" })}
                >
                  {t("col.size")}
                </th>
                <th
                  class="sortable"
                  style="width:150px"
                  onClick={() => onChange({ sort: "mtime", dir: s.sort === "mtime" && s.dir === "asc" ? "desc" : "asc" })}
                >
                  {t("col.mtime")}
                </th>
                <th style="width:110px">{t("col.kind")}</th>
              </tr>
            </thead>
            <tbody>
              {range.start > 0 && (
                <tr style={`height:${range.start * ROW_H}px`}>
                  <td colSpan={5} style="padding:0;border:0" />
                </tr>
              )}
              {slice.map((e, i) => {
                const idx = range.start + i;
                const selected = s.selection.has(e.name);
                const ic = iconClassForName(e.name, e.kind);
                return (
                  <tr
                    key={e.name}
                    class={"row" + (selected ? " selected" : "") + (idx === s.cursor ? " cursor" : "")}
                    draggable={true}
                    onDragStart={(ev: DragEvent) => {
                      const dt = ev.dataTransfer;
                      const paths = selected ? [...s.selection] : [e.name];
                      if (dt) {
                        dt.setData("text/trove", JSON.stringify(paths));
                        dt.effectAllowed = "copyMove";
                      }
                      props.onDragStartEntries(paths);
                    }}
                    onClick={(ev: MouseEvent) => clickRow(ev, idx)}
                    onDblClick={() => props.onOpenEntry(e)}
                    onContextMenu={(ev: MouseEvent) => {
                      ev.preventDefault();
                      ev.stopPropagation();
                      if (!selected) onChange({ selection: new Set([e.name]), cursor: idx });
                      props.onContext(ev, e);
                    }}
                  >
                    <td>
                      <input
                        type="checkbox"
                        class="tv-checkbox"
                        checked={selected}
                        onClick={(ev: MouseEvent) => {
                          ev.stopPropagation();
                          clickRow({ ctrlKey: true, shiftKey: false, metaKey: false } as MouseEvent, idx);
                        }}
                      />
                    </td>
                    <td>
                      <div class="tv-cellname">
                        <span class={"tv-ic " + ic}>
                          {IconFor(ic === "dir" ? "dir" : ic, { size: 16 })}
                        </span>
                        <span class="nm" title={e.name}>
                          {e.name}
                          {e.kind === "symlink" && e.target && (
                            <span class="tgt"> → {e.target}</span>
                          )}
                        </span>
                      </div>
                    </td>
                    <td class="tv-num">{e.kind === "dir" ? "—" : formatBytes(e.size)}</td>
                    <td class="tv-num">{formatTime(e.mtime)}</td>
                    <td class="tv-dim">{kindLabel(e)}</td>
                  </tr>
                );
              })}
              {range.end < visible.length && (
                <tr style={`height:${(visible.length - range.end) * ROW_H}px`}>
                  <td colSpan={5} style="padding:0;border:0" />
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {/* status bar */}
      <div class="tv-statusbar">
        <span>
          {s.searchHits !== null
            ? t("ft.items", { n: s.searchHits.length })
            : t("ft.items", { n: s.total }) + (s.entries.length < s.total ? ` (${s.entries.length})` : "")}
        </span>
        <span>{selCount > 0 ? t("ft.selected", { n: selCount, bytes: formatBytes(selBytes) }) : ""}</span>
      </div>
    </div>
  );
}

function SearchResultsTable(d: {
  hits: { path: string; kind: string; size: number; mtime: string }[];
  onOpen: (p: string) => void;
}) {
  return (
    <div class="tv-tablewrap">
      <table class="tv-table">
        <thead>
          <tr>
            <th style="width:34px" />
            <th>{t("col.name")}</th>
            <th style="width:110px">{t("col.size")}</th>
            <th style="width:150px">{t("col.mtime")}</th>
            <th style="width:110px">{t("col.kind")}</th>
          </tr>
        </thead>
        <tbody>
          {d.hits.map((h) => (
            <tr key={h.path} class="row" onDblClick={() => d.onOpen(h.path)}>
              <td>
                <span class={"tv-ic " + h.kind}>
                  {IconFor(h.kind, { size: 16 })}
                </span>
              </td>
              <td>
                <div class="tv-cellname">
                  <span class="nm" title={h.path}>
                    {h.path.split("/").pop()}
                  </span>
                  <span class="tgt">{h.path}</span>
                </div>
              </td>
              <td class="tv-num">{h.kind === "dir" ? "—" : formatBytes(h.size)}</td>
              <td class="tv-num">{formatTime(h.mtime)}</td>
              <td class="tv-dim">{h.kind}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function kindLabel(e: Entry): string {
  switch (e.kind) {
    case "dir":
      return t("kind.dir");
    case "file":
      return t("kind.file");
    case "symlink":
      return t("kind.symlink");
    default:
      return t("kind.other");
  }
}

function buildCrumbs(
  root: RootRef | null,
  path: string,
): { label: string; home?: boolean; path: string }[] {
  if (!root) return [];
  const out: { label: string; home?: boolean; path: string }[] = [];
  out.push({ label: root.id, home: path === "", path: "" });
  let acc = "";
  for (const seg of path.split("/").filter(Boolean)) {
    acc = acc ? acc + "/" + seg : seg;
    out.push({ label: seg, home: false, path: acc });
  }
  return out;
}
