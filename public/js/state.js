const host = location.origin.replace(/^http/, 'ws') + '/ws';
const CHUNK_SIZE = 256 * 1024;
const MAX_IN_FLIGHT = 3;

let sendState = null;
let recvState = null;

function formatSpeed(bytesPerSec) {
    if (!isFinite(bytesPerSec) || bytesPerSec <= 0) return "0 KB/s";
    if (bytesPerSec >= 1024 * 1024) return (bytesPerSec / (1024 * 1024)).toFixed(2) + " MB/s";
    return (bytesPerSec / 1024).toFixed(2) + " KB/s";
}

function formatTime(seconds) {
    if (!isFinite(seconds) || seconds < 0) return "--:--";
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = Math.floor(seconds % 60);
    return (h > 0 ? h + ":" : "") + String(m).padStart(2, '0') + ":" + String(s).padStart(2, '0');
}

function toHex(buffer) {
    return Array.from(new Uint8Array(buffer)).map(b => b.toString(16).padStart(2, '0')).join('');
}

async function hash(file) {
    const buffer = await file.arrayBuffer();
    return toHex(await crypto.subtle.digest('SHA-256', buffer));
}

function updateTransferUI(prefix, doneBytes, totalBytes, startedAt) {
    if (!totalBytes || totalBytes <= 0) return;
    const bar = document.getElementById(prefix + 'Bar');
    const speedEl = document.getElementById(prefix + 'Speed');
    const etaEl = document.getElementById(prefix + 'Eta');

    const percent = Math.min(100, (doneBytes / totalBytes) * 100);
    bar.style.width = percent + '%';

    const elapsed = Math.max(0.1, (performance.now() - startedAt) / 1000);
    const speed = doneBytes / elapsed;
    const leftBytes = Math.max(0, totalBytes - doneBytes);
    const eta = speed > 0 ? leftBytes / speed : Infinity;

    speedEl.innerText = `Tốc độ: ${formatSpeed(speed)}`;
    etaEl.innerText = `Còn lại: ${formatTime(eta)}`;
}

function postSend(msg) {
    document.getElementById('sendMsg').innerText = msg;
}

function postRecv(msg) {
    document.getElementById('recvMsg').innerText = msg;
}

function resetSendUI() {
    document.getElementById('sendBar').style.width = '0%';
    document.getElementById('sendSpeed').innerText = 'Tốc độ: 0 KB/s';
    document.getElementById('sendEta').innerText = 'Còn lại: --:--';
}

function resetRecvUI() {
    document.getElementById('recvBar').style.width = '0%';
    document.getElementById('recvSpeed').innerText = 'Tốc độ: 0 KB/s';
    document.getElementById('recvEta').innerText = 'Còn lại: --:--';
    document.getElementById('recvSaveBtn').style.display = 'none';
    document.getElementById('recvSaveBtn').innerText = 'Lưu File Này';
}

function closeSocketQuietly(ws) {
    if (!ws) return;
    try { ws.close(); } catch (_) {}
}
