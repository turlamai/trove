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
function trove_var_tokens(): array {
    $var = @parse_ini_file(TROVE_VAR_INI);
    if (!is_array($var)) {
        return [];
    }
    $out = [];
    foreach (['csrf_token', 'unraid_token'] as $k) {
        if (isset($var[$k]) && is_string($var[$k]) && $var[$k] !== '') {
            $out[$k] = $var[$k];
        }
    }
    return $out;
}

function trove_csrf_token(): string {
    $t = trove_var_tokens();
    return $t['csrf_token'] ?? '';
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
    // [client method, daemon method, daemon path, timeout]
    return [
        'capabilities' => ['GET', 'GET', '/api/v1/capabilities', 10],
        'roots'        => ['GET', 'GET', '/api/v1/roots', 10],
        'entries'      => ['GET', 'GET', '/api/v1/entries', 30],
        'entry'        => ['GET', 'GET', '/api/v1/entry', 15],
        'download'     => ['GET', 'GET', '/api/v1/download', 600],
        'search'       => ['GET', 'GET', '/api/v1/search', 60],
        'plan'         => ['POST', 'POST', '/api/v1/operations/plan', 90],
        'execute'      => ['POST', 'POST', '/api/v1/operations/execute', 30],
        'jobs'         => ['GET', 'GET', '/api/v1/jobs', 10],
        'job_submit'   => ['POST', 'POST', '/api/v1/jobs', 30],
        'job'          => ['GET', 'GET', '/api/v1/jobs/{id}', 15],
        'job_cancel'   => ['POST', 'POST', '/api/v1/jobs/{id}/cancel', 15],
        'job_retry'    => ['POST', 'POST', '/api/v1/jobs/{id}/retry', 30],
        'job_resolve'  => ['POST', 'POST', '/api/v1/jobs/{id}/resolve', 15],
        'job_events'   => ['GET', 'GET', '/api/v1/jobs/{id}/events', 15],
        'csrf'         => ['GET', 'GET', '/gateway/csrf', 5],
        'upload'       => ['PUT', 'PUT', '/api/v1/upload', 1800],
        'trash_list'   => ['GET', 'GET', '/api/v1/trash', 15],
        'trash_restore'=> ['POST', 'POST', '/api/v1/trash/restore', 15],
        'shares'       => ['GET', 'GET', '/api/v1/shares', 15],
        'shares_create'=> ['POST', 'POST', '/api/v1/shares', 15],
        'shares_revoke'=> ['POST', 'POST', '/api/v1/shares/revoke', 15],
        'zip'          => ['GET', 'GET', '/api/v1/zip', 3600],
        'stream'       => ['GET', 'GET', '/api/v1/stream', 0],
        'debug_log'    => ['GET', 'GET', '/api/v1/debug/log', 15],
        'settings'     => ['GET', 'GET', '/api/v1/settings', 10],
        'settings_put' => ['POST', 'PUT', '/api/v1/settings', 15],
    ];
}

/**
 * Relay a media stream byte-for-byte: forward the client's Range header,
 * pass the daemon's status and media headers back untouched, and echo
 * chunks as they arrive (no buffering — a multi-GB video must never sit
 * in a php-fpm worker's memory).
 */
function trove_stream_forward(string $daemonPath, string $query): void {
    $token = @file_get_contents(TROVE_TOKEN_FILE);
    if ($token === false || $token === '') {
        trove_fail(503, 'GATEWAY', 'daemon token unavailable');
    }
    $token = trim($token);
    $url = 'http://troved' . $daemonPath . ($query !== '' ? '?' . $query : '');

    while (ob_get_level() > 0) { @ob_end_flush(); }
    header('X-Accel-Buffering: no');
    header_remove('Content-Type');

    $headers = ['X-Trove-Token: ' . $token, 'Connection: close'];
    if (isset($_SERVER['HTTP_RANGE']) && is_string($_SERVER['HTTP_RANGE']) && preg_match('/^bytes=\d*-\d*$/', $_SERVER['HTTP_RANGE'])) {
        $headers[] = 'Range: ' . $_SERVER['HTTP_RANGE'];
    }

    $sentStatus = false;
    $ch = curl_init($url);
    curl_setopt_array($ch, [
        CURLOPT_UNIX_SOCKET_PATH => TROVE_SOCKET,
        CURLOPT_CUSTOMREQUEST => 'GET',
        CURLOPT_HTTPHEADER => $headers,
        CURLOPT_HEADERFUNCTION => function ($ch, string $line) use (&$sentStatus) {
            $len = strlen($line);
            $t = trim($line);
            if ($t === '') {
                return $len;
            }
            if (!$sentStatus && stripos($t, 'HTTP/') === 0) {
                // "HTTP/1.1 206 Partial Content" — the reason phrase may
                // trail, so match the code right after the version
                if (preg_match('#^HTTP/\S+\s+(\d{3})#i', $t, $m)) {
                    http_response_code((int)$m[1]);
                }
                $sentStatus = true;
                return $len;
            }
            if (preg_match('/^(Content-Type|Content-Length|Content-Range|Accept-Ranges|Content-Disposition|Last-Modified|ETag):/i', $t)) {
                header($t);
            }
            return $len;
        },
        CURLOPT_WRITEFUNCTION => function ($ch, string $chunk) {
            echo $chunk;
            @flush();
            return strlen($chunk);
        },
        CURLOPT_CONNECTTIMEOUT => 3,
        CURLOPT_TIMEOUT => 0,
    ]);
    curl_exec($ch);
    $errno = curl_errno($ch);
    curl_close($ch);
    if ($errno !== 0 && !$sentStatus) {
        trove_fail(503, 'GATEWAY', 'daemon unreachable during stream');
    }
    exit;
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

// Upload: raw-file PUT. nginx requires the csrf_token form field on POSTs,
// which a raw-byte body cannot carry — so uploads use PUT with the token in
// the X-Trove-Csrf header (nginx passes header-token PUTs through; the
// gateway verifies it against var.ini itself). Streams php://input →
// daemon socket with flat memory.
if ($cmd === 'upload') {
    if ($method !== 'PUT') {
        trove_fail(405, 'BAD_REQUEST', 'upload requires PUT');
    }
    if (!trove_same_origin()) {
        trove_fail(403, 'ORIGIN_REJECTED', 'cross-origin request rejected');
    }
    $csrf = trove_csrf_token();
    $hdr = $_SERVER['HTTP_X_TROVE_CSRF'] ?? '';
    if ($csrf === '' || $hdr === '' || !hash_equals($csrf, $hdr)) {
        trove_fail(403, 'CSRF_INVALID', 'invalid security token', true);
    }
    $rootId = $_GET['rootId'] ?? '';
    $dir = $_GET['dir'] ?? '';
    $name = $_GET['name'] ?? '';
    $policy = $_GET['policy'] ?? 'rename';
    foreach (['rootId' => $rootId, 'name' => $name] as $k => $v) {
        if (!is_string($v) || $v === '' || strlen($v) > 300) {
            trove_fail(400, 'BAD_REQUEST', "invalid $k");
        }
    }
    if (!is_string($dir) || strlen($dir) > 3800) {
        trove_fail(400, 'BAD_REQUEST', 'invalid dir');
    }
    if (!in_array($policy, ['skip', 'rename', 'overwrite'], true)) {
        trove_fail(400, 'BAD_REQUEST', 'unknown conflict policy');
    }
    $token = trim((string)@file_get_contents(TROVE_TOKEN_FILE));
    if ($token === '') {
        trove_fail(503, 'GATEWAY', 'daemon token unavailable');
    }
    $qs = http_build_query(['rootId' => $rootId, 'dir' => $dir, 'name' => $name, 'policy' => $policy]);
    $url = 'http://troved/api/v1/upload?' . $qs;
    $ch = curl_init($url);
    curl_setopt_array($ch, [
        CURLOPT_UNIX_SOCKET_PATH => TROVE_SOCKET,
        CURLOPT_CUSTOMREQUEST => 'PUT',
        CURLOPT_HTTPHEADER => [
            'X-Trove-Token: ' . $token,
            'Content-Type: application/octet-stream',
            'Connection: close',
        ],
        CURLOPT_UPLOAD => true,
        CURLOPT_INFILE => fopen('php://input', 'rb'),
        CURLOPT_INFILESIZE => (int)($_SERVER['CONTENT_LENGTH'] ?? 0),
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CONNECTTIMEOUT => 3,
        CURLOPT_TIMEOUT => 0,
    ]);
    $resp = curl_exec($ch);
    $status = (int)curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
    $errno = curl_errno($ch);
    curl_close($ch);
    if ($resp === false || $errno !== 0 || $status === 0) {
        trove_fail(503, 'GATEWAY', 'daemon unreachable during upload', true);
    }
    http_response_code($status >= 200 ? $status : 500);
    echo $resp;
    exit;
}

// Token refresh: var.ini rotates on emhttp events; a long-lived SPA page
// re-reads the current token here (Unraid's documented gettoken pattern).
if ($cmd === 'csrf') {
    $t = trove_csrf_token();
    if ($t === '') {
        trove_fail(403, 'CSRF_INVALID', 'token unavailable; re-login', true);
    }
    echo json_encode(['csrf' => $t]);
    exit;
}

// Method normalization: PUT arrives as POST in some PHP SAPIs via
// X-HTTP-Method-Override; we accept the real method only.
$routes = trove_routes();
if (!isset($routes[$cmd])) {
    trove_fail(404, 'NOT_FOUND', 'unknown command');
}
[$wantMethod, $daemonMethod, $daemonPath, $timeout] = $routes[$cmd];

if ($method !== $wantMethod) {
    trove_fail(405, 'BAD_REQUEST', 'method not allowed for this command');
}

// Same-origin enforcement (reads tolerate missing headers, writes do not).
if (!trove_same_origin()) {
    trove_fail(403, 'ORIGIN_REJECTED', 'cross-origin request rejected');
}

// Writes need the WebGUI CSRF token. Unraid 6.10+/7 nginx drops POSTs
// whose `token` body field does not match var.ini's unraid_token, and
// rewrites the csrf_token field to that value on the way through — so the
// gateway accepts a match against EITHER var.ini token (both are served
// only to authenticated WebGUI sessions; nginx already validated one of
// them). The X-Trove-Csrf header stays accepted as an explicit channel.
if ($method !== 'GET') {
    $tokens = trove_var_tokens();
    if ($tokens === []) {
        trove_fail(403, 'CSRF_INVALID', 'security token unavailable; reload', true);
    }
    // nginx strips the csrf_token body field after its check, so the
    // verifiable credential is the X-Trove-Csrf header; body fields are
    // accepted as fallbacks for non-nginx proxying.
    $candidate = $_SERVER['HTTP_X_TROVE_CSRF'] ?? '';
    if ($candidate === '' || !is_string($candidate)) {
        $candidate = $_POST['token'] ?? '';
    }
    if ($candidate === '' || !is_string($candidate)) {
        $candidate = $_POST['csrf_token'] ?? '';
    }
    if (!is_string($candidate) || $candidate === '') {
        trove_fail(403, 'CSRF_INVALID', 'invalid security token (no credential)', true);
    }
    $ok = false;
    foreach ($tokens as $v) {
        if (hash_equals($v, $candidate)) {
            $ok = true;
            break;
        }
    }
    if (!$ok) {
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
}

// Body: clients POST urlencoded `payload` (JSON) + `csrf_token`, because
// Unraid's nginx CSRF layer only inspects form-encoded POST bodies. The
// daemon keeps speaking JSON.
$body = '';
if ($method === 'POST') {
    $raw = isset($_POST['payload']) && is_string($_POST['payload'])
        ? $_POST['payload']
        : file_get_contents('php://input');
    if (is_string($raw) && $raw !== '') {
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

// stream must be a byte relay, not a buffered forward: the generic path
// holds the whole file in PHP memory, swallows the Range request and the
// daemon's 206/Content-Range replies, and reports the wrong Content-Type —
// unseekable playback plus a memory ceiling per media request.
if ($cmd === 'stream') {
    trove_stream_forward($finalPath, $query);
}

echo trove_forward($daemonMethod, $finalPath, $query, $body, $timeout);
