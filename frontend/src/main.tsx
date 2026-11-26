// Entry: mounts Trove inside the Unraid page container, reading boot
// configuration injected by the PHP gateway (API base, CSRF token, version).
import { render } from "preact";
import { initApi } from "./api";
import { App } from "./App";
import { AppProvider } from "./store";
import { watchTheme } from "./theme";
import "./styles/trove.css";

interface Boot {
  api: string;
  csrf: string;
  version: string;
}

declare global {
  interface Window {
    __TROVE_BOOT__?: Partial<Boot>;
  }
}

function mount() {
  const el = document.getElementById("trove-root");
  if (!el) {
    console.error("[trove] #trove-root not found");
    return;
  }
  const boot = window.__TROVE_BOOT__ ?? {};
  initApi({
    api: boot.api ?? "/plugins/trove/api.php",
    csrf: boot.csrf ?? "",
    version: boot.version ?? "dev",
  });

  const applyTheme = (dark: boolean) => {
    el.classList.toggle("trove--dark", dark);
  };
  watchTheme(el, applyTheme);

  render(
    <AppProvider>
      <App />
    </AppProvider>,
    el,
  );
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", mount);
} else {
  mount();
}
