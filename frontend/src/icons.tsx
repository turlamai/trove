// Inline SVG line icons — one consistent 24×24 stroke style, colored via
// currentColor so Unraid themes apply automatically.
interface P {
  size?: number;
  class?: string;
}

const S = ({ size = 16, d, fill }: { size?: number; d: string; fill?: boolean }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill={fill ? "currentColor" : "none"}
    stroke="currentColor"
    stroke-width="1.8"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d={d} />
  </svg>
);

export const IconFolder = (p: P) => (
  <S {...p} d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z" />
);
export const IconFile = (p: P) => (
  <S {...p} d="M6 3h8l4 4v14a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z M14 3v4h4" />
);
export const IconVideo = (p: P) => (
  <S {...p} d="M4 5h16a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1z M10 9l5 3-5 3V9z" />
);
export const IconImage = (p: P) => (
  <S {...p} d="M4 5h16a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1z M8 11a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3z M21 15l-5-4-9 7" />
);
export const IconAudio = (p: P) => (
  <S {...p} d="M9 18V6l10-2v12 M9 18a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z M19 16a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0z" />
);
export const IconArchive = (p: P) => (
  <S {...p} d="M4 4h16v5H4V4z M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9 M10 13h4" />
);
export const IconSubtitle = (p: P) => (
  <S {...p} d="M4 5h16a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1z M7 14h6 M15 14h2 M7 17h3 M12 17h5" />
);
export const IconLink = (p: P) => (
  <S {...p} d="M10 14a5 5 0 0 0 7 0l2-2a5 5 0 0 0-7-7l-1 1 M14 10a5 5 0 0 0-7 0l-2 2a5 5 0 0 0 7 7l1-1" />
);
export const IconHome = (p: P) => (
  <S {...p} d="M4 11l8-7 8 7 M6 9.5V20h12V9.5 M10 20v-5h4v5" />
);
export const IconClock = (p: P) => (
  <S {...p} d="M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z M12 7v5l3 3" />
);
export const IconStar = (p: P) => (
  <S {...p} d="M12 3l2.7 5.6 6.3.8-4.6 4.3 1.2 6.1L12 17l-5.6 2.8 1.2-6.1L3 9.4l6.3-.8L12 3z" />
);
export const IconDisk = (p: P) => (
  <S {...p} d="M4 6a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6z M8 4v6l2-1.5L12 10V4 M7 16h10 M7 19h4" />
);
export const IconSSD = (p: P) => (
  <S {...p} d="M5 4h14a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z M8 8h8 M8 12h8 M8 16h4" />
);
export const IconCopy = (p: P) => (
  <S {...p} d="M9 9h10a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1V10a1 1 0 0 1 1-1z M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1" />
);
export const IconMove = (p: P) => (
  <S {...p} d="M4 12h13 M13 6l6 6-6 6" />
);
export const IconTrash = (p: P) => (
  <S {...p} d="M5 7h14 M10 7V5h4v2 M7 7l1 13h8l1-13 M10 11v5 M14 11v5" />
);
export const IconPencil = (p: P) => (
  <S {...p} d="M4 20l1-4L16 5l3 3L8 19l-4 1z M14 7l3 3" />
);
export const IconRefresh = (p: P) => (
  <S {...p} d="M20 12a8 8 0 1 1-2.3-5.7 M20 3v4h-4" />
);
export const IconPlus = (p: P) => <S {...p} d="M12 5v14 M5 12h14" />;
export const IconSettings = (p: P) => (
  <S {...p} d="M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z M19 12a7 7 0 0 0-.1-1.2l2-1.5-2-3.4-2.4 1a7 7 0 0 0-2-1.2L14 3h-4l-.5 2.7a7 7 0 0 0-2 1.2l-2.4-1-2 3.4 2 1.5a7 7 0 0 0 0 2.4l-2 1.5 2 3.4 2.4-1a7 7 0 0 0 2 1.2L10 21h4l.5-2.7a7 7 0 0 0 2-1.2l2.4 1 2-3.4-2-1.5c.1-.4.1-.8.1-1.2z" />
);
export const IconList = (p: P) => (
  <S {...p} d="M9 6h11 M9 12h11 M9 18h11 M4.5 6h.01 M4.5 12h.01 M4.5 18h.01" />
);
export const IconGrid = (p: P) => (
  <S {...p} d="M4 4h7v7H4V4z M13 4h7v7h-7V4z M4 13h7v7H4v-7z M13 13h7v7h-7v-7z" />
);
export const IconDual = (p: P) => (
  <S {...p} d="M3 5h18v14H3V5z M12 5v14" />
);
export const IconSingle = (p: P) => <S {...p} d="M3 5h18v14H3V5z" />;
export const IconClose = (p: P) => <S {...p} d="M6 6l12 12 M18 6L6 18" />;
export const IconCheck = (p: P) => <S {...p} d="M4 12l5 5L20 7" />;
export const IconWarn = (p: P) => (
  <S {...p} d="M12 3L2 20h20L12 3z M12 10v4 M12 17.5v.01" />
);
export const IconError = (p: P) => (
  <S {...p} d="M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z M9 9l6 6 M15 9l-6 6" />
);
export const IconInfo = (p: P) => (
  <S {...p} d="M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z M12 8v.01 M12 11v6" />
);
export const IconChevronDown = (p: P) => <S {...p} d="M6 9l6 6 6-6" />;
export const IconChevronRight = (p: P) => <S {...p} d="M9 6l6 6-6 6" />;
export const IconSearch = (p: P) => (
  <S {...p} d="M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13z M15.5 15.5L21 21" />
);
export const IconUpload = (p: P) => (
  <S {...p} d="M12 20V9 M7 14l5-5 5 5 M5 4h14" />
);
export const IconDownload = (p: P) => (
  <S {...p} d="M12 4v11 M7 10l5 5 5-5 M5 20h14" />
);
export const IconHash = (p: P) => (
  <S {...p} d="M5 9h14 M5 15h14 M9 3L7 21 M17 3l-2 18" />
);
export const IconCancel = (p: P) => (
  <S {...p} d="M5 5l14 14 M19 5L5 19" />
);
export const IconShield = (p: P) => (
  <S {...p} d="M12 3l7 3v5c0 5-3.5 8.5-7 10-3.5-1.5-7-5-7-10V6l7-3z M9 12l2 2 4-4" />
);
export const IconTasks = (p: P) => (
  <S {...p} d="M8 7h4 M8 12h9 M8 17h6 M15 4l4 3-4 3" />
);

export function IconFor(kind: string, p: P) {
  switch (kind) {
    case "dir":
      return <IconFolder {...p} />;
    case "video":
      return <IconVideo {...p} />;
    case "image":
      return <IconImage {...p} />;
    case "audio":
      return <IconAudio {...p} />;
    case "archive":
      return <IconArchive {...p} />;
    case "subtitle":
      return <IconSubtitle {...p} />;
    case "symlink":
      return <IconLink {...p} />;
    default:
      return <IconFile {...p} />;
  }
}

export function iconClassForName(name: string, kind: string): string {
  if (kind === "dir") return "dir";
  const n = name.toLowerCase();
  if (/\.(mp4|mkv|avi|mov|wmv|flv|webm|m4v|ts|iso|vob)$/.test(n)) return "video";
  if (/\.(jpg|jpeg|png|gif|webp|bmp|svg|heic|cr2|nef)$/.test(n)) return "image";
  if (/\.(mp3|flac|wav|aac|ogg|m4a|ape|dsf)$/.test(n)) return "audio";
  if (/\.(zip|rar|7z|tar|gz|xz|bz2|tgz|iso)$/.test(n)) return "archive";
  if (/\.(srt|ass|ssa|sub|sup|vtt)$/.test(n)) return "subtitle";
  return "file";
}
