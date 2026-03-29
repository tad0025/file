async function verifyAndNotifyIfReady() {
    const r = recvState;
    if (!r || r.hashVerified || !r.transferDone || !r.expectedHash) return;

    r.hashVerified = true;
    postRecv('Đang so hash...');

    try {
        const savedFile = await r.fileHandle.getFile();
        const receivedHash = await hash(savedFile);
        const expected = String(r.expectedHash).toLowerCase();
        const ok = receivedHash === expected;

        if (ok) {
            postRecv('Thành công: đã lưu file và hash khớp.');
        } else {
            postRecv(`Đã lưu file nhưng lệch hash. Nhận: ${receivedHash}`);
        }

        if (r.ws.readyState === WebSocket.OPEN) {
            r.ws.send(JSON.stringify({
                type: 'recv_done',
                ok,
                expectedHash: expected,
                receivedHash,
                bytes: r.receivedBytes
            }));
            closeSocketQuietly(r.ws);
        }
    } catch (err) {
        postRecv('Lỗi so hash: ' + err.message);
    }
}

async function openSavePickerForMeta(r) {
    if (!r || !r.meta) return false;
    try {
        r.fileHandle = await window.showSaveFilePicker({
            suggestedName: r.meta.name || `relay-${Date.now()}.bin`
        });
        r.writable = await r.fileHandle.createWritable();
        return true;
    } catch (err) {
        postRecv('Đã có tên file. Bấm "Lưu File Này" để chọn nơi lưu.');
        const saveBtn = document.getElementById('recvSaveBtn');
        saveBtn.style.display = 'block';
        saveBtn.innerText = `Lưu: ${r.meta.name || 'relay.bin'}`;
        return false;
    }
}

async function confirmReceiveSave() {
    const r = recvState;
    if (!r || !r.meta || r.writable) return;

    const ok = await openSavePickerForMeta(r);
    if (!ok) return;

    document.getElementById('recvSaveBtn').style.display = 'none';
    r.startedAt = performance.now();
    postRecv('Đang nhận: ' + r.meta.name);
    if (r.ws.readyState === WebSocket.OPEN) {
        r.ws.send(JSON.stringify({ type: 'ready', window: MAX_IN_FLIGHT }));
    }
}

async function receive() {
    const tid = document.getElementById('tid').value.trim();
    if (!tid) return alert('Nhập mã Transfer ID trước');

    closeSocketQuietly(recvState?.ws);
    resetRecvUI();

    const ws = new WebSocket(`${host}?role=receiver&tid=${encodeURIComponent(tid)}`);
    ws.binaryType = 'arraybuffer';

    recvState = {
        ws,
        meta: null,
        fileHandle: null,
        writable: null,
        writeChain: Promise.resolve(),
        receivedBytes: 0,
        startedAt: 0,
        transferDone: false,
        expectedHash: '',
        hashVerified: false,
        closingStarted: false
    };

    ws.onopen = () => postRecv('Đã kết nối. Đang chờ metadata...');

    ws.onmessage = async (e) => {
        const r = recvState;
        if (!r || r.ws !== ws) return;

        if (typeof e.data === 'string') {
            let msg;
            try { msg = JSON.parse(e.data); } catch (_) { return; }

            if (msg.type === 'meta') {
                r.meta = msg;
                postRecv('Đã nhận tên file: ' + msg.name);
                const ok = await openSavePickerForMeta(r);
                if (!ok) return;
                document.getElementById('recvSaveBtn').style.display = 'none';
                r.startedAt = performance.now();
                postRecv('Đang nhận: ' + msg.name);
                ws.send(JSON.stringify({ type: 'ready', window: MAX_IN_FLIGHT }));
                return;
            }

            if (msg.type === 'hash') {
                r.expectedHash = String(msg.value || '').toLowerCase();
                await verifyAndNotifyIfReady();
                return;
            }

            return;
        }

        if (!r.meta || !r.writable || r.closingStarted) return;

        const chunk = new Uint8Array(e.data);
        r.receivedBytes += chunk.byteLength;
        updateTransferUI('recv', r.receivedBytes, r.meta.size, r.startedAt);

        ws.send(JSON.stringify({ type: 'ack', bytes: chunk.byteLength }));

        r.writeChain = r.writeChain.then(() => r.writable.write(chunk));

        if (r.receivedBytes >= r.meta.size && !r.closingStarted) {
            r.closingStarted = true;
            document.getElementById('recvEta').innerText = 'Còn lại: 00:00';
            postRecv('Đã nhận đủ dữ liệu, đang đóng file...');

            await r.writeChain;
            await r.writable.close();
            r.transferDone = true;

            await verifyAndNotifyIfReady();
            if (!r.expectedHash) {
                postRecv('Đã nhận xong, đang chờ hash từ bên gửi để xác thực...');
            }
        }
    };

    ws.onerror = () => postRecv('Lỗi kết nối bên nhận.');
    ws.onclose = async () => {
        const r = recvState;
        if (r && r.ws === ws) {
            try {
                if (r.writable && !r.transferDone) {
                    await r.writable.close();
                }
            } catch (_) {}
            recvState = null;
        }
    };
}

window.receive = receive;
window.confirmReceiveSave = confirmReceiveSave;
