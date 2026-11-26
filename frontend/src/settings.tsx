// In-app settings view. UI preferences persist locally; daemon settings
// (data dir, concurrency, physical-write) persist through the daemon into
// the plugin cfg file shared with the Unraid settings page.
import { useEffect, useState } from "preact/hooks";
import { api, Settings } from "./api";
import { t } from "./i18n";
import { useApp } from "./store";

export function SettingsView() {
  const { prefs, setPrefs, toast, caps } = useApp();
  const [srv, setSrv] = useState<Settings | null>(null);
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    api.settings().then(setSrv).catch(() => setSrv(null));
  }, []);

  const patch = (p: Partial<Settings>) => {
    if (!srv) return;
    setSrv({ ...srv, ...p });
    setDirty(true);
  };

  const save = async () => {
    if (!srv) return;
    try {
      await api.saveSettings({
        dataDir: srv.dataDir,
        concurrency: srv.concurrency,
        physicalWrite: srv.physicalWrite,
        protectedMode: srv.protectedMode,
        expertMode: srv.expertMode,
        showHidden: srv.showHidden,
        language: prefs.lang,
      });
      toast(t("set.saved") + " · " + t("set.needsReload"));
      setDirty(false);
    } catch (e) {
      toast(String(e), "err");
    }
  };

  return (
    <div class="tv-settings">
      <h3>{t("set.title")}</h3>
      <div class="desc">Trove · {caps?.version ?? ""}</div>

      <div class="tv-field">
        <label>{t("set.language")}</label>
        <select
          class="tv-input"
          style="max-width:200px"
          value={prefs.lang}
          onChange={(ev: Event) => setPrefs({ lang: (ev.target as HTMLSelectElement).value as "zh-CN" | "en" })}
        >
          <option value="zh-CN">简体中文</option>
          <option value="en">English</option>
        </select>
      </div>

      <div class="tv-switchline">
        <input
          type="checkbox"
          class="tv-checkbox"
          checked={prefs.showHidden}
          onChange={(ev: Event) => setPrefs({ showHidden: (ev.target as HTMLInputElement).checked })}
        />
        <label>{t("set.showHidden")}</label>
      </div>
      <div class="tv-switchline">
        <input
          type="checkbox"
          class="tv-checkbox"
          checked={prefs.dualDefault}
          onChange={(ev: Event) => setPrefs({ dualDefault: (ev.target as HTMLInputElement).checked })}
        />
        <label>{t("set.dualDefault")}</label>
      </div>

      <hr style="border:0;border-top:1px solid var(--tv-border);margin:18px 0" />

      {srv && (
        <>
          <div class="tv-field">
            <label>{t("set.dataDir")}</label>
            <input
              class="tv-input mono"
              type="text"
              placeholder="/mnt/user/trove-data"
              value={srv.dataDir}
              onInput={(ev: InputEvent) => patch({ dataDir: (ev.target as HTMLInputElement).value })}
            />
            <div class="hint" style="color:var(--tv-text-muted);font-size:12px;margin-top:4px">
              {t("set.dataDirHint")}
            </div>
          </div>
          <div class="tv-field" style="max-width:200px">
            <label>{t("set.concurrency")}</label>
            <select
              class="tv-input"
              value={String(srv.concurrency)}
              onChange={(ev: Event) => patch({ concurrency: Number((ev.target as HTMLSelectElement).value) })}
            >
              {[1, 2, 3, 4].map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </div>
          <div class="tv-switchline">
            <input
              type="checkbox"
              class="tv-checkbox"
              checked={srv.expertMode}
              onChange={(ev: Event) => patch({ expertMode: (ev.target as HTMLInputElement).checked })}
            />
            <label>
              {t("set.expert")}
              <div class="hint">{t("nav.devices")}</div>
            </label>
          </div>
          <div class="tv-switchline">
            <input
              type="checkbox"
              class="tv-checkbox"
              checked={srv.physicalWrite}
              onChange={(ev: Event) => patch({ physicalWrite: (ev.target as HTMLInputElement).checked })}
            />
            <label>
              {t("set.physicalWrite")}
              <div class="hint">/mnt/diskN · /mnt/&lt;pool&gt;</div>
            </label>
          </div>
          <div class="tv-field">
            <label>{t("set.protectedMode")}</label>
            <select
              class="tv-input"
              style="max-width:280px"
              value={srv.protectedMode}
              onChange={(ev: Event) => patch({ protectedMode: (ev.target as HTMLSelectElement).value })}
            >
              <option value="deny">{t("set.protected.deny")}</option>
              <option value="ack">{t("set.protected.ack")}</option>
            </select>
          </div>
          <div style="margin-top:16px">
            <button class="tv-btn primary" disabled={!dirty} onClick={() => void save()}>
              {t("set.saved").split(" ")[0]}
            </button>
          </div>
        </>
      )}
    </div>
  );
}
