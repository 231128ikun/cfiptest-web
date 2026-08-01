// auto.js —— 自动化维护页：订阅器定义、IP 库管理、一键运行与进度
import { escapeHTML } from './columns.js';
import * as api from './api.js';

const $ = id => document.getElementById(id);
const fmtTime = ts => {
    if (!ts) return '—';
    const d = new Date(ts);
    if (Number.isNaN(d.getTime())) return '—';
    const diff = Date.now() - d.getTime();
    if (diff < 60_000) return '刚刚';
    if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
    if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
    return d.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
};
const fmtSpeed = kbs => (kbs > 0 ? `${Math.round(kbs)} kB/s` : '—');
const fmtLatency = ms => (ms > 0 ? `${ms} ms` : '—');
const SOURCE_LABEL = { manual: '手动', import: '导入', official: '官方', topup: '补足' };
const STATUS_LABEL = { active: '有效', new: '未测' };

export function initAuto({ toast }) {
    const state = {
        subs: [],
        libEntries: [],
        libTotal: 0,
        selected: new Set(),
        running: false,
        report: null,
    };

    function log(line, kind = '') {
        const box = $('auto-log');
        const div = document.createElement('div');
        div.className = `auto-log-line ${kind}`;
        div.textContent = `[${new Date().toLocaleTimeString('zh-CN', { hour12: false })}] ${line}`;
        box.appendChild(div);
        box.scrollTop = box.scrollHeight;
    }

    function setRunning(running) {
        state.running = running;
        $('btn-auto-run').disabled = running;
        $('btn-auto-stop').disabled = !running;
        $('auto-run-status').textContent = running ? '运行中…' : '';
    }

    function showReport(report) {
        state.report = report;
        const box = $('auto-report');
        box.hidden = !report;
        if (!report) return;
        const rows = (report.groups || []).map(g => `
            <tr>
                <td>${escapeHTML(g.name || '')}</td>
                <td>${g.filled ?? 0} / ${g.target ?? 0}</td>
                <td class="${g.shortage > 0 ? 'bad-shortage' : ''}">${g.shortage ?? 0}</td>
                <td>${g.tested ?? 0}</td>
                <td>${g.failed ?? 0}</td>
                <td>${g.speedTested ?? 0}</td>
                <td>${g.speedFailed ?? 0}</td>
                <td>${g.updated ?? 0}</td>
            </tr>`).join('');
        const shortages = (report.shortages || []).map(s => `<div class="auto-shortage">⚠ ${escapeHTML(s)}</div>`).join('');
        const link = report.outputPath
            ? `<div class="auto-report-row">输出文件：<a href="${api.autoOutputUrl(report.outputPath.replace(/\\/g, '/').replace(/^.*[\\/]data[\\/]/, 'out/'))}" download>下载 ${escapeHTML(report.outputPath)}</a></div>`
            : '';
        box.innerHTML = `
            <div class="auto-report-head">本次运行（${Math.round((report.durationMs ?? 0) / 1000)}s）</div>
            <table class="results auto-report-table">
                <thead><tr><th>分组</th><th>配额</th><th>缺口</th><th>延迟测试</th><th>延迟失败(移除)</th><th>测速</th><th>测速失败(保留)</th><th>回写更新</th></tr></thead>
                <tbody>${rows}</tbody>
            </table>
            ${shortages}
            ${link}
            <div class="auto-report-row">共输出 ${report.totalLines ?? 0} 行，移除失效 ${report.removedDead ?? 0} 条</div>`;
    }

    // ---- 订阅器 ----
    async function loadSubs() {
        try {
            const data = await api.fetchAutoSubs();
            state.subs = data.subscriptions || [];
            $('auto-subs-editor').value = JSON.stringify(state.subs, null, 2);
            const select = $('auto-run-select');
            select.innerHTML = state.subs.map(s => `<option value="${escapeHTML(s.name)}">${escapeHTML(s.name)}</option>`).join('')
                || '<option value="">（暂无订阅器，先在上方添加）</option>';
            $('auto-run-select').disabled = state.subs.length === 0;
        } catch (error) {
            toast(`加载订阅器失败：${error.message}`);
        }
    }

    async function saveSubs() {
        let parsed;
        try {
            parsed = JSON.parse($('auto-subs-editor').value);
        } catch (error) {
            toast(`JSON 格式错误：${error.message}`);
            return;
        }
        try {
            await api.saveAutoSubs(Array.isArray(parsed) ? parsed : [parsed]);
            toast('订阅器已保存');
            $('auto-subs-status').textContent = '已保存到 data/subscriptions.json';
            await loadSubs();
        } catch (error) {
            toast(`保存失败：${error.message}`);
            $('auto-subs-status').textContent = error.message;
        }
    }

    async function validateSubs() {
        let parsed;
        try {
            parsed = JSON.parse($('auto-subs-editor').value);
        } catch (error) {
            toast(`JSON 格式错误：${error.message}`);
            return;
        }
        const list = Array.isArray(parsed) ? parsed : [parsed];
        for (const sub of list) {
            try {
                await api.validateAutoSub(sub);
            } catch (error) {
                toast(`「${sub.name || '未命名'}」校验失败：${error.message}`);
                return;
            }
        }
        toast(`校验通过（${list.length} 个订阅器）`);
    }

    // ---- IP 库 ----
    async function loadLibrary() {
        const params = {
            q: $('auto-lib-q').value.trim(),
            status: $('auto-lib-status').value,
            country: $('auto-lib-country').value,
            limit: 500,
        };
        try {
            const data = await api.fetchAutoLibrary(params);
            state.libEntries = data.entries || [];
            state.libTotal = data.total || 0;
            renderLibTable();
            renderLibStats(data.stats);
            renderCountryFilter(data.stats);
        } catch (error) {
            toast(`加载 IP 库失败：${error.message}`);
        }
    }

    function renderLibStats(stats) {
        if (!stats) return;
        $('auto-lib-total').textContent = `库 ${stats.total} 条（有效 ${stats.active} / 未测 ${stats.new}）`;
    }

    function renderCountryFilter(stats) {
        const select = $('auto-lib-country');
        const current = select.value;
        const codes = Object.keys(stats?.byCountry || {}).filter(c => c && c !== 'unknown').sort();
        const options = ['<option value="">全部国家</option>']
            .concat(codes.map(c => `<option value="${escapeHTML(c)}">${escapeHTML(c)}</option>`))
            .join('');
        select.innerHTML = options;
        select.value = current;
    }

    function renderLibTable() {
        const tbody = $('auto-lib-tbody');
        if (state.libEntries.length === 0) {
            tbody.innerHTML = `<tr class="pad"><td colspan="10" class="auto-lib-empty">库为空：先在下方导入一些 IP，或直接运行维护由订阅器补足</td></tr>`;
            $('auto-lib-count').textContent = `共 ${state.libTotal} 条`;
            return;
        }
        tbody.innerHTML = state.libEntries.map(e => {
            const key = `${e.ip}|${e.port || 0}`;
            const checked = state.selected.has(key) ? 'checked' : '';
            return `<tr>
                <td><input type="checkbox" class="auto-lib-check" data-key="${escapeHTML(key)}" ${checked} aria-label="选择 ${escapeHTML(e.ip)}"></td>
                <td class="mono">${escapeHTML(e.ip)}</td>
                <td>${e.port || '—'}</td>
                <td>${escapeHTML(e.emoji || '')}${escapeHTML(e.country || e.countryCode || '—')}</td>
                <td>${escapeHTML(e.cityZh || '—')}</td>
                <td>${fmtLatency(e.tcpLatencyMs)}</td>
                <td>${fmtSpeed(e.speedValid ? e.speedKBs : 0)}</td>
                <td>${STATUS_LABEL[e.status] || escapeHTML(e.status || '—')}</td>
                <td>${SOURCE_LABEL[e.source] || escapeHTML(e.source || '—')}</td>
                <td>${fmtTime(e.lastCheckedAt)}</td>
            </tr>`;
        }).join('');
        $('auto-lib-count').textContent = `共 ${state.libTotal} 条，当前显示 ${state.libEntries.length} 条`;
    }

    async function importLib() {
        const source = $('auto-lib-source').value;
        const text = $('auto-lib-text').value.trim();
        if (!text) {
            toast('请先粘贴要导入的 IP');
            return;
        }
        try {
            const result = await api.importAutoLibrary({ text, source });
            toast(`已入库：新增 ${result.added} 条，更新 ${result.updated} 条（库共 ${result.total} 条）`);
            $('auto-lib-text').value = '';
            await loadLibrary();
        } catch (error) {
            toast(`导入失败：${error.message}`);
        }
    }

    async function removeSelected() {
        const keys = [...state.selected];
        if (keys.length === 0) {
            toast('请先勾选要移除的条目');
            return;
        }
        try {
            const result = await api.removeAutoLibrary(keys);
            toast(`已移除 ${result.removed} 条`);
            state.selected.clear();
            await loadLibrary();
        } catch (error) {
            toast(`移除失败：${error.message}`);
        }
    }

    async function clearLib() {
        if (!confirm('确认清空整个 IP 库？此操作不可恢复。')) return;
        try {
            await api.clearAutoLibrary();
            toast('IP 库已清空');
            state.selected.clear();
            await loadLibrary();
        } catch (error) {
            toast(`清空失败：${error.message}`);
        }
    }

    // ---- 运行 ----
    async function run() {
        const name = $('auto-run-select').value;
        if (!name) {
            toast('请先在上方定义并保存订阅器');
            return;
        }
        try {
            const result = await api.runAuto(name);
            state.report = null;
            $('auto-report').hidden = true;
            $('auto-output-link').hidden = true;
            setRunning(true);
            log(`启动维护：${name}（taskId=${result.taskId}）`);
            log('正在收集候选…', 'info');
        } catch (error) {
            toast(`启动失败：${error.message}`);
            log(`启动失败：${error.message}`, 'error');
        }
    }

    function stop() {
        api.stopTask('').catch(() => {});
        log('正在停止…', 'warn');
    }

    function onAuto(message) {
        if (!message) return;
        let p;
        try {
            p = JSON.parse(message);
        } catch {
            return;
        }
        if (p.stage === 'report' && p.report) {
            showReport(p.report);
            return;
        }
        const prefix = p.group ? `[${p.group}] ` : '';
        switch (p.stage) {
            case 'gather':
                log(`${prefix}收集候选 ${p.tested} 条`, 'info');
                break;
            case 'latency':
                log(`${prefix}延迟检测完成：通过 ${p.passed}，失败 ${p.failed}（已从库移除）`, p.failed > 0 ? 'warn' : 'ok');
                break;
            case 'speed':
                log(`${prefix}测速完成：有效 ${p.tested - p.failed}，失败 ${p.failed}（保留待下次验证）`, p.failed > 0 ? 'warn' : 'ok');
                break;
            case 'output':
                log(`${prefix}${p.log || '已写出订阅文件'}`, 'ok');
                break;
            default:
                if (p.log) log(`${prefix}${p.log}`);
        }
    }

    function onDone(message, reason) {
        if (!state.running) return;
        setRunning(false);
        if (reason === 'stopped') {
            log('已停止', 'warn');
        } else if (message && message.startsWith('自动化完成')) {
            log(message, 'ok');
            $('auto-run-status').textContent = '完成';
            $('auto-output-link').hidden = false;
        } else {
            log(message || '运行结束', 'error');
        }
        loadLibrary();
        loadSubs();
    }

    // ---- 事件绑定 ----
    $('btn-auto-sample').addEventListener('click', () => {
        const sample = [{
            name: '示例订阅',
            enableSpeed: false,
            groups: [
                { name: '美国', countryCode: 'US', country: '美国', count: 10, maxLatencyMs: 300 },
                { name: '日本', countryCode: 'JP', country: '日本', count: 10, maxLatencyMs: 300 },
                { name: '新加坡', countryCode: 'SG', country: '新加坡', count: 10, maxLatencyMs: 300 },
            ],
            output: { path: 'out/示例订阅.txt', format: 'txt', template: '{ip}:{port}#{emoji}{country}' },
        }];
        $('auto-subs-editor').value = JSON.stringify(sample, null, 2);
        toast('已填入示例，可修改后保存');
    });
    $('btn-auto-validate').addEventListener('click', validateSubs);
    $('btn-auto-save').addEventListener('click', saveSubs);
    $('btn-auto-lib-import').addEventListener('click', importLib);
    $('btn-auto-lib-refresh').addEventListener('click', loadLibrary);
    $('btn-auto-lib-remove').addEventListener('click', removeSelected);
    $('btn-auto-lib-clear').addEventListener('click', clearLib);
    $('btn-auto-run').addEventListener('click', run);
    $('btn-auto-stop').addEventListener('click', stop);

    ['auto-lib-q', 'auto-lib-status', 'auto-lib-country'].forEach(id => {
        $(id).addEventListener('input', loadLibrary);
    });
    $('auto-lib-status').addEventListener('change', loadLibrary);
    $('auto-lib-country').addEventListener('change', loadLibrary);

    $('auto-lib-tbody').addEventListener('change', event => {
        const box = event.target.closest('.auto-lib-check');
        if (!box) return;
        if (box.checked) state.selected.add(box.dataset.key);
        else state.selected.delete(box.dataset.key);
    });
    $('auto-lib-checkall').addEventListener('change', event => {
        const checked = event.target.checked;
        state.selected.clear();
        if (checked) state.libEntries.forEach(e => state.selected.add(`${e.ip}|${e.port || 0}`));
        renderLibTable();
    });

    // 页面加载时同步运行状态
    api.fetchTaskStatus().then(status => {
        if (status.status === 'running' && (status.taskId || '').startsWith('auto:')) {
            setRunning(true);
            log('检测到自动化任务正在后台运行…', 'info');
        }
    }).catch(() => {});

    loadSubs();
    loadLibrary();

    return { onAuto, onDone, isAutoRunning: () => state.running, refreshLibrary: loadLibrary };
}
