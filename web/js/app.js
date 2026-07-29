// app.js —— 主控：初始化各模块、编排流水线步骤

import { processInput, quickDeduplicate, smartFilter, getInputStats } from './input.js';
import * as api from './api.js';
import { ResultTable, CSV_COLUMNS } from './table.js';
import { PRESETS, formatResults, placeholderNames } from './composer.js';
import { downloadAsText, downloadAsCSV, copyToClipboard } from './exporter.js';

const $ = id => document.getElementById(id);

/* ---------------- 全局状态 ---------------- */
let currentTaskId = null;
let currentTaskType = null; // 'latency' | 'speed'
let eventSource = null;
let table = null;
let defaults = null;

/* ---------------- Toast ---------------- */
let toastTimer = null;
function toast(msg) {
    const el = $('toast');
    el.textContent = msg;
    el.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.remove('show'), 2200);
}

/* ---------------- 阶段 1：输入整理 ---------------- */

function cleanedLines() {
    return $('ip-cleaned').value.split('\n').map(l => l.trim()).filter(Boolean);
}

function refreshInputStats() {
    const lines = cleanedLines();
    const stats = getInputStats(lines);
    $('stat-valid').textContent = stats.total;
    $('stat-v4').textContent = stats.v4;
    $('stat-v6').textContent = stats.v6;
    const portText = Object.entries(stats.ports)
        .sort((a, b) => b[1] - a[1])
        .slice(0, 6)
        .map(([p, n]) => `${p}×${n}`)
        .join(' ');
    $('stat-ports').innerHTML = portText ? `端口分布 <b>${portText}</b>` : '';
    $('btn-start-latency').disabled = stats.total === 0 || currentTaskId !== null;
}

function parseCleanedToTargets() {
    return cleanedLines().map(line => {
        // IPv6: [addr]:port；IPv4: addr:port
        const m = line.match(/^\[([0-9a-fA-F:]+)\]:(\d+)$/);
        if (m) return { ip: m[1], port: parseInt(m[2], 10) };
        const idx = line.lastIndexOf(':');
        return { ip: line.slice(0, idx), port: parseInt(line.slice(idx + 1), 10) };
    });
}

function bindInputStage() {
    $('btn-normalize').addEventListener('click', () => {
        const { valid, invalidCount, dupCount } = processInput($('ip-input').value);
        $('ip-cleaned').value = valid.join('\n');
        refreshInputStats();
        toast(`整理完成：${valid.length} 条有效（丢弃 ${invalidCount}，去重 ${dupCount}）`);
    });

    $('btn-dedupe').addEventListener('click', () => {
        const lines = $('ip-input').value.split('\n');
        const deduped = quickDeduplicate(lines);
        $('ip-cleaned').value = deduped.join('\n');
        refreshInputStats();
        toast(`去重完成：${lines.filter(l => l.trim()).length} → ${deduped.length}`);
    });

    const doFilter = mode => {
        const expr = $('filter-expr').value;
        const current = cleanedLines();
        if (!current.length) { toast('请先整理输入'); return; }
        const filtered = smartFilter(current, expr, mode);
        if (!filtered) { toast('筛选表达式无效'); return; }
        $('ip-cleaned').value = filtered.join('\n');
        refreshInputStats();
        toast(`筛选完成：${current.length} → ${filtered.length}`);
    };
    $('btn-filter-keep').addEventListener('click', () => doFilter('keep'));
    $('btn-filter-remove').addEventListener('click', () => doFilter('remove'));
}

/* ---------------- 阶段 2：测试执行 ---------------- */

function latencyOptions() {
    return {
        maxConcurrency: parseInt($('lat-concurrency').value, 10) || undefined,
        timeoutMs: parseInt($('lat-timeout').value, 10) || undefined,
        maxLatencyMs: parseInt($('lat-maxlatency').value, 10) || 0,
        enableTLS: $('lat-tls').checked,
        enableIPAPI: $('lat-ipapi').checked,
    };
}

function speedOptions() {
    return {
        maxConcurrency: parseInt($('spd-concurrency').value, 10) || undefined,
        durationSec: parseInt($('spd-duration').value, 10) || undefined,
        minSpeedKBs: parseFloat($('spd-minspeed').value) || 0,
        downloadURL: $('spd-url').value.trim() || undefined,
        enableTLS: $('spd-tls').checked,
    };
}

function setRunning(running, type) {
    currentTaskId = running ? currentTaskId : null;
    currentTaskType = running ? type : null;
    $('btn-start-latency').disabled = running || cleanedLines().length === 0;
    $('btn-start-speed').disabled = running || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = running || table.getAllResults().length === 0;
    $('btn-stop').disabled = !running;
    $('progress-wrap').classList.toggle('active', running);
    if (!running) {
        $('progress-label').textContent = '已完成';
    }
}

function updateProgress(p) {
    const pct = p.total ? Math.round((p.completed / p.total) * 100) : 0;
    $('progress-fill').style.width = `${pct}%`;
    $('progress-pct').textContent = `${pct}%`;
    const label = currentTaskType === 'speed' ? '测速中' : '延迟测试中';
    $('progress-label').textContent = `${label} ${p.completed}/${p.total} · 有效 ${p.validIPs}`;
}

function bindTestStage() {
    $('btn-start-latency').addEventListener('click', async () => {
        const targets = parseCleanedToTargets();
        if (!targets.length) { toast('请先整理输入列表'); return; }
        table.clear();
        $('result-count').textContent = '';
        try {
            const resp = await api.startLatencyTest(targets, latencyOptions());
            currentTaskId = resp.taskId;
            setRunning(true, 'latency');
            $('progress-label').textContent = `延迟测试 0/${resp.totalTargets}`;
        } catch (e) {
            toast(e.message);
        }
    });

    const startSpeed = async useFiltered => {
        const results = useFiltered ? table.getAllResults() : table.getSelectedResults();
        const targets = results.map(r => ({ ip: r.ip, port: r.port }));
        if (!targets.length) { toast(useFiltered ? '没有可测速的结果' : '请先勾选要测速的结果'); return; }
        try {
            const resp = await api.startSpeedTest(targets, speedOptions());
            currentTaskId = resp.taskId;
            setRunning(true, 'speed');
            $('progress-label').textContent = `测速 0/${resp.totalTargets}`;
        } catch (e) {
            toast(e.message);
        }
    };
    $('btn-start-speed').addEventListener('click', () => startSpeed(false));
    $('btn-speed-filtered').addEventListener('click', () => startSpeed(true));

    $('btn-stop').addEventListener('click', async () => {
        try {
            await api.stopTask(currentTaskId);
            toast('已发送停止指令');
        } catch (e) {
            toast(e.message);
        }
    });
}

/* ---------------- SSE 事件 ---------------- */

function bindEvents() {
    eventSource = api.subscribeEvents({
        onResult: r => {
            table.appendResult(r);
            $('result-count').textContent = `（已发现 ${table.results.length} 个有效 IP）`;
        },
        onProgress: updateProgress,
        onSpeed: r => table.updateSpeed(r),
        onDone: msg => {
            setRunning(false);
            refreshButtons();
            toast(msg);
            $('result-count').textContent = `（共 ${table.results.length} 个有效 IP）`;
        },
        onError: msg => {
            // EventSource 断线也会触发；仅在任务运行时提示
            if (currentTaskId) {
                setRunning(false);
                toast(msg || '任务出错');
            }
        },
    });

    // 关页面时收掉 SSE 连接：EventSource 不关会自动重连，
    // 刷新页面时后端会短暂多出一个订阅者。
    window.addEventListener('beforeunload', () => {
        eventSource?.close();
        eventSource = null;
    });
}

function refreshButtons() {
    $('btn-start-speed').disabled = currentTaskId !== null || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = currentTaskId !== null || table.getAllResults().length === 0;
    $('btn-append').disabled = table.getSelectedResults().length === 0 && table.getAllResults().length === 0;
}

/* ---------------- 阶段 3：结果表格 ---------------- */

function bindResultsStage() {
    table = new ResultTable($('result-table-container'));

    $('result-filter').addEventListener('input', e => table.setFilter(e.target.value));

    // 勾选变化时刷新按钮。ResultTable 在勾选后派发 selectionchange，
    // 无需再靠 setTimeout 等重绘结束（改造前勾选会重建整表）。
    $('result-table-container').addEventListener('selectionchange', refreshButtons);

    // 国家配额
    $('btn-quota-toggle').addEventListener('click', () => {
        const box = $('quota-box');
        const show = !box.classList.contains('active');
        if (show) renderQuotaGrid();
        box.classList.toggle('active', show);
    });
    $('btn-quota-apply').addEventListener('click', () => {
        const quotas = {};
        document.querySelectorAll('#quota-grid .quota-item').forEach(item => {
            const n = parseInt(item.querySelector('input').value, 10);
            if (n > 0) quotas[item.dataset.country] = n;
        });
        table.applyCountryQuotas(Object.keys(quotas).length ? quotas : null);
        refreshButtons();
        toast(Object.keys(quotas).length ? `已按配额选择 ${table.getSelectedResults().length} 条` : '已清除配额选择');
    });
    $('btn-quota-clear').addEventListener('click', () => {
        table.clearSelection();
        refreshButtons();
    });

    // 追加到结果框
    $('btn-append').addEventListener('click', () => {
        let selected = table.getSelectedResults();
        if (!selected.length) selected = table.getAllResults();
        if (!selected.length) { toast('没有可追加的结果'); return; }
        const template = $('format-template').value;
        const text = formatResults(template, selected);
        const box = $('output-box');
        box.value = box.value.trim() ? `${box.value.trim()}\n${text}` : text;
        toast(`已追加 ${selected.length} 条到结果框`);
    });
}

function renderQuotaGrid() {
    const stats = table.getCountryStats();
    $('quota-grid').innerHTML = stats.length
        ? stats.map(s => `
            <span class="quota-item" data-country="${s.name}">
                ${s.emoji} ${s.name} <span class="count">(${s.count})</span>
                <input type="number" min="0" max="${s.count}" placeholder="0">
            </span>`).join('')
        : '<span style="color:var(--text-secondary);font-size:12px">暂无结果</span>';
}

/* ---------------- 阶段 4：导出 ---------------- */

function bindExportStage() {
    // 预设
    const sel = $('format-presets');
    sel.innerHTML = '<option value="">选择预设格式…</option>' +
        PRESETS.map(p => `<option value="${p.template}">${p.name}</option>`).join('');
    sel.addEventListener('change', () => {
        if (sel.value) $('format-template').value = sel.value;
    });

    // 占位符帮助（点击插入）
    $('placeholder-help').innerHTML = '可用占位符（点击插入）：' +
        placeholderNames().map(p => `<code data-ph="${p}">${p}</code>`).join('');
    $('placeholder-help').addEventListener('click', e => {
        const ph = e.target.dataset?.ph;
        if (!ph) return;
        const input = $('format-template');
        input.value += ph;
        input.focus();
    });

    // 结果框工具
    $('btn-output-dedupe').addEventListener('click', () => {
        const lines = $('output-box').value.split('\n').map(l => l.trim()).filter(Boolean);
        const deduped = [...new Set(lines)];
        $('output-box').value = deduped.join('\n');
        toast(`去重：${lines.length} → ${deduped.length}`);
    });
    $('btn-output-clear').addEventListener('click', () => {
        $('output-box').value = '';
    });

    // 复制与下载
    $('btn-copy').addEventListener('click', async () => {
        const text = $('output-box').value.trim();
        if (!text) { toast('结果框为空'); return; }
        await copyToClipboard(text);
        toast('已复制到剪贴板');
    });
    $('btn-download-txt').addEventListener('click', () => {
        const text = $('output-box').value.trim();
        if (!text) { toast('结果框为空'); return; }
        downloadAsText(text);
    });
    $('btn-download-csv').addEventListener('click', () => {
        let results = table.getSelectedResults();
        if (!results.length) results = table.getAllResults();
        if (!results.length) { toast('没有可导出的结果'); return; }
        const cols = CSV_COLUMNS.filter(c =>
            document.querySelector(`#csv-columns input[data-key="${c.key}"]`)?.checked);
        if (!cols.length) { toast('请至少选择一列'); return; }
        downloadAsCSV(results, cols, { enableTLS: $('lat-tls').checked });
        toast(`已导出 ${results.length} 条 × ${cols.length} 列`);
    });

    // CSV 列选择器（列与列数都由注册表派生，不再硬编码「共 27 列」）
    $('csv-columns').innerHTML = CSV_COLUMNS.map(c =>
        `<label class="checkbox"><input type="checkbox" data-key="${c.key}" checked> ${c.label}</label>`
    ).join('');
    $('csv-column-count').textContent = CSV_COLUMNS.length;
}

/* ---------------- 初始化 ---------------- */

async function init() {
    bindInputStage();
    bindTestStage();
    bindResultsStage();
    bindExportStage();
    bindEvents();
    refreshInputStats();

    try {
        const cfg = await api.fetchConfig();
        defaults = cfg.defaults;
        $('app-version').textContent = cfg.version || '未知版本';
        $('lat-concurrency').value = defaults.latency.maxConcurrency;
        $('lat-timeout').value = defaults.latency.timeoutMs;
        $('lat-maxlatency').value = defaults.latency.maxLatencyMs;
        $('lat-tls').checked = defaults.latency.enableTLS;
        $('lat-ipapi').checked = defaults.latency.enableIPAPI;
        $('spd-concurrency').value = defaults.speed.maxConcurrency;
        $('spd-duration').value = defaults.speed.durationSec;
        $('spd-minspeed').value = defaults.speed.minSpeedKBs;
        $('spd-url').value = defaults.speed.downloadURL;
        $('spd-tls').checked = defaults.speed.enableTLS;

        const el = $('res-status');
        el.textContent = `地理位置 ${cfg.locationCount} 条 · ASN ${cfg.asnLoaded ? '已加载' : '未加载'}`;
        el.classList.add('ok');
    } catch (e) {
        $('res-status').textContent = '无法连接后端：' + e.message;
    }
}

init();
