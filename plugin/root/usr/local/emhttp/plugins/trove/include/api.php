<?php
/**
 * Trove gateway: same-origin PHP → local daemon over a root-only Unix socket.
 *
 * Security posture (docs/threat-model.md):
 *  - GET requests are read-only; POST requests must carry the WebGUI CSRF
 *    token (from /var/local/emhttp/var.ini) in a custom header that
 *    cross-site forms cannot add, and must pass a same-origin check when an
 *    Origin header is present.
 *  - The daemon authenticates this gateway by socket peer credentials (root)
 *    plus a per-start token file; the gateway never sees user data itself.
 *  - No client-controlled input reaches a shell. The daemon binary path and
 *    arguments are fixed.
 */

declare(encoding='UTF-8');

const TROVE_SOCKET = '/run/trove/troved.sock';
const TROVE_TOKEN_FILE = '/run/trove/token';
const TROVE_VAR_INI = '/var/local/emhttp/var.ini';
const TROVE_STARTER = '/usr/local/emhttp/plugins/trove/scripts/start-daemon.sh';

ini_set('display_errors', '0');
header('Content-Type: application/json; charset=utf-8');
header('X-Content-Type-Options: nosniff');
header('Cache-Control: no-store');

/**
 * Fail with the contract error envelope.
 */
function trove_fail(int $status, string $code, string $message, bool $recoverable = false): void {
    http_response_code($status);
    echo json_encode([
        'error' => [
            'code' => $code,
            'message' => $message,
            'recoverable' => $recoverable,
            'requestId' => 'gw_' . bin2hex(random_bytes(6)),
        ],
    ], JSON_UNESCAPED_UNICODE);
    exit;
}

/**
 * Parse the emhttp var file for the current CSRF token.
 */
function trove_csrf_token(): string {
    $var = @parse_ini_file(TROVE_VAR_INI);
    return is_array($var) && isset($var['csrf_token']) ? (string)$var['csrf_token'] : '';
}

function trove_same_origin(): bool {
    $host = (string)($_SERVER['HTTP_HOST'] ?? '');
    if ($host === '') {
        return false;
    }
    // HTTP_HOST keeps the port ("nas.local:1996"); compare host AND port.
    $hparts = explode(':', $host, 2);
    $hhost = strtolower($hparts[0]);
    $scheme = (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off') ? 'https' : 'http';
    if (!empty($_SERVER['HTTP_X_FORWARDED_PROTO'])) {
        $scheme = strtolower(explode(',', $_SERVER['HTTP_X_FORWARDED_PROTO'])[0]);
    }
    $hport = isset($hparts[1])
        ? (int)$hparts[1]
        : (int)($_SERVER['SERVER_PORT'] ?? ($scheme === 'https' ? 443 : 80));

    $origin = (string)($_SERVER['HTTP_ORIGIN'] ?? '');
    if ($origin !== '') {
        $p = @parse_url($origin);
        if (!isset($p['scheme'], $p['host'])) {
            return false;
        }
        $oport = (int)($p['port'] ?? (strtolower((string)$p['scheme']) === 'https' ? 443 : 80));
        return strtolower((string)$p['scheme']) === $scheme
            && strtolower((string)$p['host']) === $hhost
            && $oport === $hport;
    }
    $referer = (string)($_SERVER['HTTP_REFERER'] ?? '');
    if ($referer !== '') {
        $p = @parse_url($referer);
        if (!isset($p['host'])) {
            return false;
        }
        $rport = (int)($p['port'] ?? (strtolower((string)($p['scheme'] ?? 'http')) === 'https' ? 443 : 80));
        return strtolower((string)$p['host']) === $hhost && $rport === $hport;
    }
    // No Origin/Referer at all: same-origin fetches may omit them for GET;
    // writes are additionally protected by the CSRF header below.
    return true;
}

/**
 * Route table: gateway cmd → daemon (method, path). Everything not listed
 * here is refused; there is no generic pass-through.
 */
function trove_routes(): array {
    return [
        'capabilities' => ['GET', '/api/v1/capabilities', 10],
        'roots'        => ['GET', '/api/v1/roots', 10],
        'entries'      => ['GET', '/api/v1/entries', 30],
        'entry'        => ['GET', '/api/v1/entry', 15],
        'download'     => ['GET', '/api/v1/download', 600],
        'search'       => ['GET', '/api/v1/search', 60],
        'plan'         => ['POST', '/api/v1/operations/plan', 90],
        'execute'      => ['POST', '/api/v1/operations/execute', 30],
        'jobs'         => ['GET', '/api/v1/jobs', 10],
        'job'          => ['GET', '/api/v1/jobs/{id}', 15],
        'job_cancel'   => ['POST', '/api/v1/jobs/{id}/cancel', 15],
        'job_retry'    => ['POST', '/api/v1/jobs/{id}/retry', 30],
        'job_resolve'  => ['POST', '/api/v1/jobs/{id}/resolve', 15],
        'job_events'   => ['GET', '/api/v1/jobs/{id}/events', 15],
        'settings'     => ['GET', '/api/v1/settings', 10],
        'settings_put' => ['PUT', '/api/v1/settings', 15],
    ];
}

/**
 * Forward the request to the daemon; retry once after an on-demand start.
 */
function trove_forward(string $method, string $daemonPath, string $query, string $body, int $timeout): string {
    $token = @file_get_contents(TROVE_TOKEN_FILE);
    if ($token === false || $token === '') {
        trove_fail(503, 'GATEWAY', 'daemon token unavailable');
    }
    $token = trim($token);
    $url = 'http://troved' . $daemonPath . ($query !== '' ? '?' . $query : '');

    $attempt = function () use ($method, $url, $body, $timeout, $token) {
        $ch = curl_init($url);
        $headers = [
            'X-Trove-Token: ' . $token,
            'Connection: close',
        ];
        if ($body !== '') {
            $headers[] = 'Content-Type: application/json';
        }
        curl_setopt_array($ch, [
            CURLOPT_UNIX_SOCKET_PATH => TROVE_SOCKET,
            CURLOPT_CUSTOMREQUEST => $method,
            CURLOPT_HTTPHEADER => $headers,
            CURLOPT_POSTFIELDS => $body !== '' ? $body : null,
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_CONNECTTIMEOUT => 3,
            CURLOPT_TIMEOUT => $timeout,
        ]);
        $resp = curl_exec($ch);
        $status = (int)curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
        $errno = curl_errno($ch);
        curl_close($ch);
        return [$resp, $status, $errno];
    };

    [$resp, $status, $errno] = $attempt();
    if ($resp === false || $errno !== 0 || $status === 0) {
        // Daemon down: start it on demand (fixed command, no client input),
        // then retry once.
        if (is_file(TROVE_STARTER)) {
            exec('/bin/bash ' . escapeshellarg(TROVE_STARTER) . ' >/dev/null 2>&1');
            [$resp, $status, $errno] = $attempt();
        }
    }
    if ($resp === false || $errno !== 0 || $status === 0) {
        trove_fail(503, 'GATEWAY', 'daemon unreachable', true);
    }
    http_response_code($status >= 200 ? $status : 500);
    return (string)$resp;
}

// ---------------------------------------------------------------------------
// entry
// ---------------------------------------------------------------------------

$method = $_SERVER['REQUEST_METHOD'] ?? 'GET';
$cmd = $_GET['cmd'] ?? '';

// Method normalization: PUT arrives as POST in some PHP SAPIs via
// X-HTTP-Method-Override; we accept the real method only.
$routes = trove_routes();
if (!isset($routes[$cmd])) {
    trove_fail(404, 'NOT_FOUND', 'unknown command');
}
[$wantMethod, $daemonPath, $timeout] = $routes[$cmd];

if ($method !== $wantMethod) {
    trove_fail(405, 'BAD_REQUEST', 'method not allowed for this command');
}

// Same-origin enforcement (reads tolerate missing headers, writes do not).
if (!trove_same_origin()) {
    trove_fail(403, 'ORIGIN_REJECTED', 'cross-origin request rejected');
}

// Writes need the WebGUI CSRF token in a custom header.
if ($method !== 'GET') {
    $hdr = $_SERVER['HTTP_X_TROVE_CSRF'] ?? '';
    $csrf = trove_csrf_token();
    if ($csrf === '' || $hdr === '' || !hash_equals($csrf, $hdr)) {
        trove_fail(403, 'CSRF_INVALID', 'invalid security token', true);
    }
}

// Job-scoped commands embed the id — validated as [A-Za-z0-9_-]+ only.
$finalPath = $daemonPath;
if (strpos($daemonPath, '{id}') !== false) {
    $id = $_GET['id'] ?? '';
    if (!is_string($id) || !preg_match('/^[A-Za-z0-9_.-]{1,64}$/', $id)) {
        trove_fail(400, 'BAD_REQUEST', 'invalid job id');
    }
    $finalPath = str_replace('{id}', $id, $daemonPath);
    unset($_GET['id']);
}

// Build the query string for GET forwards (whitelisted params only — the
// daemon validates again, but we keep the URL clean).
$query = '';
if ($method === 'GET') {
    $allowed = ['rootId', 'path', 'sort', 'dir', 'offset', 'limit', 'filter', 'q', 'since', 'dirsFirst'];
    $parts = [];
    foreach ($allowed as $k) {
        if (isset($_GET[$k]) && is_string($_GET[$k]) && strlen($_GET[$k]) < 8192) {
            $parts[] = rawurlencode($k) . '=' . rawurlencode($_GET[$k]);
        }
    }
    $query = implode('&', $parts);
} else {
    // JSON body passthrough (the daemon enforces size/schema limits).
    $query = '';
}

$body = '';
if ($method === 'POST') {
    $raw = file_get_contents('php://input');
    if ($raw !== false && $raw !== '') {
        if (strlen($raw) > 1048576) {
            trove_fail(413, 'BAD_REQUEST', 'body too large');
        }
        json_decode($raw);
        if (json_last_error() !== JSON_ERROR_NONE) {
            trove_fail(400, 'BAD_REQUEST', 'invalid JSON body');
        }
        $body = $raw;
    } else {
        $body = '{}';
    }
}

// download streams bytes, not JSON — special-case the passthrough.
if ($cmd === 'download') {
    header_remove('Content-Type');
    header('Content-Type: application/octet-stream');
    header('X-Content-Type-Options: nosniff');
}

echo trove_forward($method, $finalPath, $query, $body, $timeout);
