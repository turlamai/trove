// Follows the Unraid WebGUI theme. Unraid 7 sets html classes like
// Theme--white / Theme--black; legacy themes switch stylesheets. We detect
// both the class and the effective body background luminance, then expose
// dark mode as a `.trove--dark` class on our root container.

let observer: MutationObserver | null = null;
let current = false;
const listeners = new Set<(dark: boolean) => void>();

function luminance(hex: string): number {
  const m = hex.match(/^#?([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})/i);
  if (!m) return 1;
  const [r, g, b] = [1, 2, 3].map((i) => parseInt(m[i], 16) / 255);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function detect(): boolean {
  const html = document.documentElement;
  if (html.classList.contains("Theme--black") || html.classList.contains("dark")) return true;
  if (html.classList.contains("Theme--white")) return false;
  // legacy dynamix themes: inspect the effective body background
  const bg = getComputedStyle(document.body).backgroundColor;
  if (bg && bg.startsWith("#")) return luminance(bg) < 0.4;
  if (bg && bg.startsWith("rgb")) {
    const parts = bg.match(/\d+/g);
    if (parts && parts.length >= 3) {
      const [r, g, b] = parts.slice(0, 3).map((x) => parseInt(x, 10) / 255);
      return 0.2126 * r + 0.7152 * g + 0.0722 * b < 0.4;
    }
  }
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

export function watchTheme(el: HTMLElement, onChange: (dark: boolean) => void) {
  void el;
  current = detect();
  listeners.add(onChange);
  onChange(current);
  if (observer) return;
  observer = new MutationObserver(() => {
    const next = detect();
    if (next !== current) {
      current = next;
      listeners.forEach((fn) => fn(current));
    }
  });
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
  document.body &&
    observer.observe(document.body, { attributes: true, attributeFilter: ["class", "style"] });
}

export function isDark(): boolean {
  return current;
}
