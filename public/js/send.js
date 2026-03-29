function pumpSend() {
    const s = sendState;
    if (!s || !s.ws || s.ws.readyState !== WebSocket.OPEN) return;

    while (s.credits > 0 && s.inFlight < MAX_IN_FLIGHT && s.offset < s.file.size) {
        const end = Math.min(s.file.size, s.offset + CHUNK_SIZE);
        const chunk = s.file.slice(s.offset, end);
        s.ws.send(chunk);

        const bytes = end - s.offset;
        s.offset = end;
        s.sentBytes += bytes;
        s.inFlight++;
        s.credits--;

        updateTransferUI('send', s.sentBytes, s.file.size, s.startedAt);
    }

    if (s.offset >= s.file.size) {
        document.getElementById('sendEta').innerText = 'Còn lại: 00:00';
    }
}

async function send() {
    const file = document.getElementById('f').files[0];
    const tid = document.getElementById('tid').value.trim();
    if (!file || !tid) return alert('Thiếu thông tin');

    closeSocketQuietly(sendState?.ws);
    resetSendUI();

    const ws = new WebSocket(`${host}?role=sender&tid=${encodeURIComponent(tid)}`);
    sendState = {
        ws,
        file,
        offset: 0,
        sentBytes: 0,
        ackedBytes: 0,
        inFlight: 0,
        credits: 0,
        startedAt: 0,
        fileHash: '',
        hashSent: false
    };

    ws.onopen = () => {
        const s = sendState;
        if (!s || s.ws !== ws) return;

        s.startedAt = performance.now();
        postSend('Đã kết nối. Chờ bên nhận sẵn sàng...');

        ws.send(JSON.stringify({
            type: 'meta',
            name: file.name,
            size: file.size,
            chunkSize: CHUNK_SIZE
        }));

        hash(file).then((value) => {
            const cur = sendState;
            if (!cur || cur.ws !== ws || ws.readyState !== WebSocket.OPEN) return;
            cur.fileHash = value;
            ws.send(JSON.stringify({ type: 'hash', value }));
            cur.hashSent = true;
        }).catch((err) => {
            postSend('Không tính được hash: ' + err.message);
        });
    };

    ws.onmessage = (e) => {
        const s = sendState;
        if (!s || s.ws !== ws) return;
        if (typeof e.data !== 'string') return;

        let msg;
        try { msg = JSON.parse(e.data); } catch (_) { return; }

        if (msg.type === 'ready') {
            const windowSize = Math.max(1, Math.min(MAX_IN_FLIGHT, Number(msg.window) || MAX_IN_FLIGHT));
            s.credits += windowSize;
            postSend('Đang gửi liên tục...');
            pumpSend();
            return;
        }

        if (msg.type === 'ack') {
            const bytes = Math.max(0, Number(msg.bytes) || 0);
            s.ackedBytes += bytes;
            s.inFlight = Math.max(0, s.inFlight - 1);
            s.credits += 1;
            pumpSend();
            return;
        }

        if (msg.type === 'recv_done') {
            if (msg.ok) {
                postSend('Hoàn tất: bên nhận lưu file và hash khớp.');
            } else {
                postSend(`Hoàn tất có lỗi hash. Sender: ${msg.expectedHash || '?'}, Receiver: ${msg.receivedHash || '?'}`);
            }
            document.getElementById('sendEta').innerText = 'Còn lại: 00:00';
            closeSocketQuietly(ws);
        }
    };

    ws.onerror = () => postSend('Lỗi kết nối bên gửi.');
    ws.onclose = () => {
        if (sendState && sendState.ws === ws) {
            sendState = null;
        }
    };
}

window.send = send;
