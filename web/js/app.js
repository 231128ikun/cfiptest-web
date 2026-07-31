// app.js —— 主流程：候选准备 → 规则执行 → 结果整理 → 格式导出

import { getInputStats, smartFilter, parseFilterExpression } from './input.js';
import * as api from './api.js';
import { store, setMode, parseLines, targetToLine } from './store.js';
import { ResultTable, CSV_COLUMNS, GROUP_DIMENSIONS } from './table.js';
import { ALL_COLUMNS, TABLE_COLUMNS, escapeHTML } from './columns.js';
import { createMultiSelect } from './multiselect.js';
import { PRESETS, formatResults, placeholderNames } from './composer.js';
import { downloadAsText, downloadAsCSV, copyToClipboard } from './exporter.js';

const $ = id => document.getElementById(id);
const keyOf = item => `${item.ip}|${item.port || 0}`;

let currentTaskId = null;
let currentTaskType = null; // pipeline | speed
let eventSource = null;
let table = null;
let defaults = null;
let officialRanges = null;
let proxyCandidates = [];
let officialCandidates = [];
let filterMode = 'keep';
let quotaPicker = null;
let quotaGroups = [];
let officialEstimateTimer = null;
let exportPreviewTimer = null;
let savedTemplates = [];

const SAVED_TEMPLATE_KEY = 'iptest.savedTemplates.v1';

const DEFAULT_COLUMN_KEYS = TABLE_COLUMNS.filter(c => c.key !== '_sel').map(c => c.key);
let visibleColumnKeys = [...DEFAULT_COLUMN_KEYS];

let toastTimer = null;
function toast(message) {
    const el = $('toast');
    el.textContent = message;
    el.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.remove('show'), 2300);
}

function addUnique(base, incoming) {
    const seen = new Set(base.map(keyOf));
    let added = 0;
    let duplicates = 0;
    for (const target of incoming) {
        if (!target?.ip) continue;
        const key = keyOf(target);
        if (seen.has(key)) { duplicates++; continue; }
        seen.add(key);
        base.push({ ip: target.ip, port: Number(target.port) || 0 });
        added++;
    }
    return { added, duplicates };
}

function activeCandidates() {
    return store.mode === 'official' ? officialCandidates : proxyCandidates;
}

function renderCandidates() {
    $('ip-candidates').value = proxyCandidates.map(targetToLine).join('\n');
    $('official-candidates').value = officialCandidates.map(targetToLine).join('\n');
    $('official-candidate-count').textContent = `${officialCandidates.length} 条`;

    const active = activeCandidates();
    $('candidate-count').textContent = `${active.length} 条`;
    $('run-target-count').textContent = active.length;
    $('btn-start-latency').disabled = currentTaskId !== null || active.length === 0;

    const stats = getInputStats(proxyCandidates.map(targetToLine));
    $('stat-v4').textContent = stats.v4;
    $('stat-v6').textContent = stats.v6;
    const unspecified = proxyCandidates.filter(t => !t.port).length;
    const ports = Object.entries(stats.ports).sort((a, b) => b[1] - a[1]).slice(0, 4)
        .map(([port, count]) => `${port}×${count}`);
    if (unspecified) ports.push(`未指定×${unspecified}`);
    $('stat-ports').textContent = ports.length ? `端口 ${ports.join(' · ')}` : '端口 —';
}

function rawLines() {
    return $('ip-input').value.split(/\r?\n/).map(line => line.trim()).filter(Boolean);
}

function selectedRawLines() {
    const lines = rawLines();
    const expression = $('filter-expr').value.trim();
    if (!expression) return lines;
    return smartFilter(lines, expression, filterMode) || lines;
}

function renderRawSummary() {
    const lines = rawLines();
    $('source-line-count').textContent = `${lines.length} 行`;
    const expression = $('filter-expr').value.trim();
    if (!expression) {
        $('filter-summary').textContent = '未设置筛选，将处理全部输入';
        $('filter-expr').style.borderColor = '';
        return;
    }
    const valid = parseFilterExpression(expression) !== null;
    $('filter-expr').style.borderColor = valid ? '' : 'var(--danger)';
    const selected = valid ? selectedRawLines().length : lines.length;
    $('filter-summary').textContent = valid
        ? `${filterMode === 'keep' ? '保留' : '排除'}匹配：当前将加入 ${selected}/${lines.length} 行`
        : '筛选表达式无效，将按全部输入处理';
}

function appendRawText(text) {
    const next = String(text || '').trim();
    if (!next) return;
    const input = $('ip-input');
    input.value = input.value.trim() ? `${input.value.trim()}\n${next}` : next;
    renderRawSummary();
}

async function addProxySelectionToCandidates() {
    const lines = selectedRawLines();
    if (!lines.length) { toast('当前没有可加入的输入'); return; }
    let parsed;
    let invalidCount = 0;
    try {
        if (lines.some(line => line.includes('/'))) {
            const resp = await api.importText(lines.join('\n'), { sampleMode: 'one', sampleN: 1 });
            parsed = resp.targets;
        } else {
            const result = parseLines(lines.join('\n'));
            parsed = result.targets;
            invalidCount = result.invalidCount;
        }
    } catch (error) {
        toast(error.message);
        return;
    }
    const { added, duplicates } = addUnique(proxyCandidates, parsed);
    renderCandidates();
    toast(`已加入候选 ${added} 条（重复 ${duplicates}，非法 ${invalidCount}）`);
}

function bindProxyInput() {
    $('ip-input').addEventListener('input', renderRawSummary);
    $('filter-expr').addEventListener('input', renderRawSummary);
    $('btn-filter-keep').addEventListener('click', () => { filterMode = 'keep'; renderRawSummary(); });
    $('btn-filter-remove').addEventListener('click', () => { filterMode = 'remove'; renderRawSummary(); });
    $('btn-filter-clear').addEventListener('click', () => {
        $('filter-expr').value = '';
        filterMode = 'keep';
        renderRawSummary();
    });
    $('btn-workspace-clear').addEventListener('click', () => {
        $('ip-input').value = '';
        renderRawSummary();
    });
    $('btn-add-paste').addEventListener('click', addProxySelectionToCandidates);
    $('btn-candidates-clear').addEventListener('click', () => {
        proxyCandidates = [];
        renderCandidates();
    });

    $('file-input').addEventListener('change', async event => {
        const file = event.target.files?.[0];
        if (!file) return;
        try {
            const text = await file.text();
            appendRawText(text);
            toast(`已加载 ${file.name}`);
        } catch (error) {
            toast(`读取文件失败：${error.message}`);
        }
        event.target.value = '';
    });

    $('btn-import-remote').addEventListener('click', async () => {
        const url = $('remote-url').value.trim();
        if (!url) { toast('请填写远程 TXT 地址'); return; }
        const button = $('btn-import-remote');
        button.disabled = true;
        button.textContent = '加载中…';
        try {
            const resp = await api.importRemote(url, { sampleMode: 'one', sampleN: 1 });
            appendRawText(resp.text || resp.targets.map(targetToLine).join('\n'));
            toast('远程 TXT 已加载到输入框');
        } catch (error) {
            toast(error.message);
        } finally {
            button.disabled = false;
            button.textContent = '加载 TXT';
        }
    });
}

function officialSettings() {
    return {
        family: document.querySelector('input[name="official-family"]:checked')?.value || 'ipv4',
        sampleMode: document.querySelector('input[name="sample-mode"]:checked')?.value || 'one',
        sampleN: parseInt($('sample-n').value, 10) || 1,
    };
}

async function fetchRanges() {
    const button = $('btn-fetch-ranges');
    button.disabled = true;
    try {
        officialRanges = await api.fetchOfficialRanges(officialSettings().sampleN);
        const source = officialRanges.source === 'builtin' ? '内置兜底' : '官方接口';
        $('ranges-status').textContent = `${source} · IPv4 ${officialRanges.ipv4.length} 段 · IPv6 ${officialRanges.ipv6.length} 段`;
        if (officialRanges.warning) toast(officialRanges.warning);
        renderRangesEstimate();
    } catch (error) {
        $('ranges-status').textContent = '加载失败';
        toast(error.message);
    } finally {
        button.disabled = false;
    }
}

function renderRangesEstimate() {
    const { family, sampleMode, sampleN } = officialSettings();
    const hint = $('sample-hint');
    hint.textContent = family === 'ipv4'
        ? 'IPv4 按每个 /24 抽样，避免直接检测百万级地址；官方模式端口固定 443。'
        : 'IPv6 网段无法穷举，将在每个官方网段内随机抽样；官方模式端口固定 443。';
    if (!officialRanges) return;
    let count;
    if (family === 'ipv4') {
        if (sampleMode === 'one') count = officialRanges.estimate?.onePerSubnet;
        else if (sampleMode === 'n') count = officialRanges.estimate?.nPerSubnet;
        else count = officialRanges.estimate?.all;
    } else {
        const segments = officialRanges.ipv6.length;
        count = segments * (sampleMode === 'one' ? 1 : sampleMode === 'n' ? sampleN : 256);
    }
    const warning = sampleMode === 'all' && family === 'ipv4' ? '，数量过大，不建议直接执行' : '';
    $('ranges-estimate').textContent = `预计生成 ${Number(count || 0).toLocaleString()} 个候选${warning}`;
}

async function generateOfficialCandidates() {
    if (!officialRanges) { await fetchRanges(); }
    if (!officialRanges) return;
    const settings = officialSettings();
    if (settings.family === 'ipv4' && settings.sampleMode === 'all') {
        toast('IPv4 全部地址超过单次展开上限，请选择抽样');
        return;
    }
    const ranges = settings.family === 'ipv6' ? officialRanges.ipv6 : officialRanges.ipv4;
    const text = ranges.map(cidr => cidr.includes(':') ? `[${cidr}]:443` : `${cidr}:443`).join('\n');
    const button = $('btn-add-ranges');
    button.disabled = true;
    try {
        const resp = await api.importText(text, settings);
        officialCandidates = [];
        const { added } = addUnique(officialCandidates, resp.targets);
        renderCandidates();
        toast(`已生成 ${added} 条官方候选`);
    } catch (error) {
        toast(error.message);
    } finally {
        button.disabled = false;
    }
}

function bindOfficialInput() {
    $('btn-fetch-ranges').addEventListener('click', fetchRanges);
    $('btn-add-ranges').addEventListener('click', generateOfficialCandidates);
    $('btn-official-clear').addEventListener('click', () => {
        officialCandidates = [];
        renderCandidates();
    });
    document.querySelectorAll('input[name="official-family"], input[name="sample-mode"]').forEach(input =>
        input.addEventListener('change', renderRangesEstimate));
    $('sample-n').addEventListener('input', () => {
        renderRangesEstimate();
        clearTimeout(officialEstimateTimer);
        officialEstimateTimer = setTimeout(async () => {
            if (!officialRanges) return;
            try {
                officialRanges = await api.fetchOfficialRanges(officialSettings().sampleN);
                renderRangesEstimate();
            } catch (error) {
                toast(error.message);
            }
        }, 250);
    });
}

function bindModes() {
    $('mode-tabs').addEventListener('click', event => {
        const tab = event.target.closest('.mode-tab');
        if (!tab) return;
        setMode(tab.dataset.mode);
        document.querySelectorAll('.mode-tab').forEach(item => {
            const active = item.dataset.mode === store.mode;
            item.classList.toggle('active', active);
            item.setAttribute('aria-selected', String(active));
        });
        $('source-proxy').hidden = store.mode !== 'proxy';
        $('source-official').hidden = store.mode !== 'official';
        renderCandidates();
    });
}

function ruleMaxResults() {
    return Math.max(0, parseInt($('rule-maxresults').value, 10) || 0);
}

function speedEnabled() { return $('spd-enable').checked; }

function latencyOptions() {
    return {
        maxConcurrency: parseInt($('lat-concurrency').value, 10) || undefined,
        timeoutMs: parseInt($('lat-timeout').value, 10) || undefined,
        maxLatencyMs: parseInt($('lat-maxlatency').value, 10) || 0,
        // 启用速度规则时，统一数量限制只在最终测速阶段生效。
        maxResults: speedEnabled() ? 0 : ruleMaxResults(),
        enableTLS: $('lat-tls').checked,
        enableIPAPI: $('lat-ipapi').checked,
    };
}

function speedOptions() {
    return {
        maxConcurrency: parseInt($('spd-concurrency').value, 10) || undefined,
        durationSec: parseInt($('spd-duration').value, 10) || undefined,
        minSpeedKBs: parseFloat($('spd-minspeed').value) || 0,
        maxResults: ruleMaxResults(),
        downloadURL: $('advanced-speed-url').value.trim() || undefined,
        enableTLS: $('lat-tls').checked,
    };
}

function applySpeedEnabled() {
    // 复选框只决定初次检测是否自动进入测速阶段。
    // 速度条件始终可编辑，并继续用于第三步的补充测速。
    refreshButtons();
}

function updateDefaultPortHint() {
    const port = $('lat-tls').checked ? 443 : 80;
    $('default-port-hint').textContent = `未指定端口使用 ${port}`;
}

function resetRules() {
    const lat = defaults?.latency || { maxConcurrency: 100, timeoutMs: 1000, maxLatencyMs: 0, enableTLS: true, enableIPAPI: false };
    const spd = defaults?.speed || { maxConcurrency: 5, durationSec: 5, minSpeedKBs: 0, downloadURL: 'speed.cloudflare.com/__down?bytes=500000000' };
    $('lat-concurrency').value = lat.maxConcurrency;
    $('lat-timeout').value = lat.timeoutMs;
    $('lat-maxlatency').value = lat.maxLatencyMs > 0 ? lat.maxLatencyMs : '';
    $('lat-tls').checked = lat.enableTLS;
    $('lat-ipapi').checked = lat.enableIPAPI;
    $('spd-enable').checked = false;
    $('spd-concurrency').value = spd.maxConcurrency;
    $('spd-duration').value = spd.durationSec;
    $('spd-minspeed').value = spd.minSpeedKBs > 0 ? spd.minSpeedKBs : '';
    $('rule-maxresults').value = '';
    $('advanced-speed-url').value = spd.downloadURL;
    $('spd-url').value = spd.downloadURL;
    $('spd-maxresults').value = 0;
    $('spd-tls').checked = lat.enableTLS;
    applySpeedEnabled();
    updateDefaultPortHint();
    toast('已恢复推荐设置');
}

function setRunning(running, type = null) {
    currentTaskType = running ? type : null;
    if (!running) currentTaskId = null;
    $('btn-start-latency').disabled = running || activeCandidates().length === 0;
    $('btn-start-speed').disabled = running || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = running || table.getAllResults().length === 0;
    $('btn-stop').disabled = !running;
    $('progress-wrap').classList.toggle('active', running);
}

function updateProgress(progress) {
    const percent = progress.total ? Math.round(progress.completed / progress.total * 100) : 0;
    $('progress-fill').style.width = `${percent}%`;
    $('progress-pct').textContent = `${percent}%`;
    const phase = progress.phase === 'speed' ? '测速' : '延迟检测';
    $('progress-label').textContent = `${phase} ${progress.completed}/${progress.total} · 符合 ${progress.validIPs}`;
}

async function startPipeline() {
    const targets = activeCandidates();
    if (!targets.length) { toast('候选区为空'); return; }
    table.clear();
    scheduleExportPreview();
    $('result-count').textContent = '';
    $('progress-fill').style.width = '0%';
    try {
        const response = await api.startLatencyTest(targets, latencyOptions(), {
            enableSpeed: speedEnabled(),
            speedOptions: speedOptions(),
        });
        currentTaskId = response.taskId;
        setRunning(true, 'pipeline');
        $('progress-label').textContent = `延迟检测 0/${response.totalTargets}`;
    } catch (error) {
        toast(error.message);
    }
}

async function startSupplementalSpeed(useVisible) {
    const results = useVisible ? table.getAllResults() : table.getSelectedResults();
    if (!results.length) { toast(useVisible ? '当前没有展示结果' : '请先勾选结果'); return; }
    try {
        const response = await api.startSpeedTest(results.map(r => ({ ip: r.ip, port: r.port })), speedOptions());
        currentTaskId = response.taskId;
        setRunning(true, 'speed');
        $('progress-label').textContent = `补充测速 0/${response.totalTargets}`;
    } catch (error) {
        toast(error.message);
    }
}

function bindRulesAndRun() {
    $('spd-enable').addEventListener('change', applySpeedEnabled);
    $('lat-tls').addEventListener('change', () => {
        $('spd-tls').checked = $('lat-tls').checked;
        updateDefaultPortHint();
    });
    $('advanced-speed-url').addEventListener('input', () => { $('spd-url').value = $('advanced-speed-url').value; });
    $('rule-maxresults').addEventListener('input', () => { $('spd-maxresults').value = ruleMaxResults(); });
    $('btn-reset-rules').addEventListener('click', resetRules);
    $('btn-start-latency').addEventListener('click', startPipeline);
    $('btn-start-speed').addEventListener('click', () => startSupplementalSpeed(false));
    $('btn-speed-filtered').addEventListener('click', () => startSupplementalSpeed(true));
    $('btn-stop').addEventListener('click', async () => {
        try {
            await api.stopTask(currentTaskId);
            toast('已发送停止指令');
        } catch (error) {
            toast(error.message);
        }
    });
}

function bindEvents() {
    eventSource = api.subscribeEvents({
        onResult: result => {
            table.appendResult(result);
            $('result-count').textContent = `（${table.results.length} 个有效节点）`;
            updateCountryFilter();
            refreshButtons();
            scheduleExportPreview();
        },
        onProgress: updateProgress,
        onSpeed: result => {
            table.updateSpeed(result);
            scheduleExportPreview();
        },
        onDone: (message, reason) => {
            setRunning(false);
            $('progress-label').textContent = reason === 'limit' ? '已达到最大数量' : reason === 'stopped' ? '已停止' : '已完成';
            $('progress-pct').textContent = reason === 'stopped' ? $('progress-pct').textContent : '100%';
            $('result-count').textContent = `（${table.results.length} 个有效节点）`;
            toast(message || '任务完成');
            refreshButtons();
            regenerateOutput();
        },
        onError: message => {
            if (!currentTaskId) return;
            setRunning(false);
            toast(message || '任务出错');
        },
    });
    window.addEventListener('beforeunload', () => eventSource?.close());
}

function selectedColumns() {
    return visibleColumnKeys.map(key => ALL_COLUMNS.find(column => column.key === key)).filter(Boolean);
}

function renderSortOptions() {
    const sortable = selectedColumns().filter(column => column.sortable);
    $('sort-key').innerHTML = sortable.map(column => `<option value="${column.key}">${escapeHTML(column.label)}</option>`).join('');
    if (!sortable.some(column => column.key === table.sortKey)) {
        table.setSort(sortable.find(column => column.key === 'tcpLatencyMs')?.key || sortable[0]?.key || 'ip', true);
    }
    $('sort-key').value = table.sortKey;
}

function renderColumnOptions() {
    const choices = ALL_COLUMNS.filter(column => column.key !== '_sel' && column.key !== 'enableTLS' && column.inCSV);
    $('column-options').innerHTML = choices.map(column => `
        <label class="checkbox"><input type="checkbox" data-key="${column.key}" ${visibleColumnKeys.includes(column.key) ? 'checked' : ''}> ${escapeHTML(column.label)}</label>`).join('');
}

function applyColumnsFromUI() {
    const keys = [...document.querySelectorAll('#column-options input:checked')].map(input => input.dataset.key);
    if (!keys.length) { toast('至少保留一个显示字段'); renderColumnOptions(); return; }
    visibleColumnKeys = keys;
    table.setColumns(keys);
    renderSortOptions();
}

function updateCountryFilter() {
    const select = $('result-country-filter');
    const current = select.value;
    const countries = table.getGroupStats('country').filter(item => item.name !== '未知');
    select.innerHTML = '<option value="">全部国家</option>' + countries.map(item =>
        `<option value="${escapeHTML(item.name)}">${escapeHTML(item.emoji)} ${escapeHTML(item.name)} (${item.count})</option>`).join('');
    if (countries.some(item => item.name === current)) select.value = current;
}

function applyResultFilters() {
    table.setFilter($('result-filter').value);
    table.setFilters({
        country: $('result-country-filter').value,
        maxLatency: parseFloat($('result-max-latency').value) || 0,
        minSpeed: parseFloat($('result-min-speed').value) || 0,
    });
    refreshButtons();
    scheduleExportPreview();
}

function currentQuotaDim() { return $('quota-dim').value || GROUP_DIMENSIONS[0].key; }

function renderQuotaGrid() {
    const stats = table.getGroupStats(currentQuotaDim(), { filtered: true });
    quotaPicker?.setItems(stats.map(item => ({
        value: item.name,
        label: item.emoji ? `${item.emoji} ${item.name}` : item.name,
        count: item.count,
    })));
    const keep = new Set(quotaGroups);
    const shown = keep.size ? stats.filter(item => keep.has(item.name)) : stats;
    $('quota-grid').innerHTML = shown.length ? shown.map(item => `
        <span class="quota-item" data-group="${escapeHTML(item.name)}">
            ${escapeHTML(item.emoji)} ${escapeHTML(item.name)} <span class="count">(${item.count})</span>
            <input type="number" min="0" max="${item.count}" placeholder="0">
        </span>`).join('') : '<span class="hint">暂无结果</span>';
}

function bindQuotaPanel() {
    $('quota-dim').innerHTML = GROUP_DIMENSIONS.map(item => `<option value="${item.key}">${item.label}</option>`).join('');
    quotaPicker = createMultiSelect($('quota-picker'), {
        placeholder: '全部分组',
        onChange: values => { quotaGroups = values; renderQuotaGrid(); },
    });
    $('quota-dim').addEventListener('change', () => { quotaGroups = []; renderQuotaGrid(); });
    $('btn-quota-toggle').addEventListener('click', () => {
        const box = $('quota-box');
        const open = !box.classList.contains('active');
        if (open) renderQuotaGrid();
        box.classList.toggle('active', open);
    });
    $('btn-quota-apply').addEventListener('click', () => {
        const quotas = {};
        document.querySelectorAll('#quota-grid .quota-item').forEach(item => {
            const count = parseInt(item.querySelector('input').value, 10) || 0;
            if (count > 0) quotas[item.dataset.group] = count;
        });
        const shown = table.applyGroupDisplayQuotas(currentQuotaDim(), quotas);
        toast(Object.keys(quotas).length ? `当前展示 ${shown} 条` : '未设置展示数量');
        refreshButtons();
        scheduleExportPreview();
    });
    $('btn-quota-clear').addEventListener('click', () => {
        table.clearDisplayQuotas();
        table.clearSelection();
        refreshButtons();
        scheduleExportPreview();
    });
}

function refreshButtons() {
    if (!table) return;
    const running = currentTaskId !== null;
    $('btn-start-speed').disabled = running || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = running || table.getAllResults().length === 0;
    refreshExportCount();
}

function bindResults() {
    table = new ResultTable($('result-table-container'));
    renderColumnOptions();
    renderSortOptions();
    bindQuotaPanel();

    $('result-filter').addEventListener('input', applyResultFilters);
    $('result-country-filter').addEventListener('change', applyResultFilters);
    $('result-max-latency').addEventListener('input', applyResultFilters);
    $('result-min-speed').addEventListener('input', applyResultFilters);
    $('sort-key').addEventListener('change', () => table.setSort($('sort-key').value, table.sortAsc));
    $('btn-sort-dir').addEventListener('click', () => table.setSort(table.sortKey, !table.sortAsc));
    $('result-table-container').addEventListener('sortchange', event => {
        $('sort-key').value = event.detail.key;
        $('btn-sort-dir').textContent = event.detail.asc ? '▲ 升序' : '▼ 降序';
        scheduleExportPreview();
    });
    $('result-table-container').addEventListener('selectionchange', () => {
        refreshButtons();
        scheduleExportPreview();
    });

    $('btn-column-toggle').addEventListener('click', () => $('column-box').classList.toggle('active'));
    $('column-options').addEventListener('change', applyColumnsFromUI);
    $('btn-column-default').addEventListener('click', () => {
        visibleColumnKeys = [...DEFAULT_COLUMN_KEYS];
        renderColumnOptions();
        table.setColumns(visibleColumnKeys);
        renderSortOptions();
    });
}

function exportScope() {
    return document.querySelector('input[name="export-scope"]:checked')?.value || 'all';
}

function exportResults() {
    if (!table) return [];
    if (exportScope() === 'selected') return table.getSelectedResults();
    if (exportScope() === 'visible') return table.getAllResults();
    return table.getResults();
}

function refreshExportCount() {
    if (!table) return;
    $('export-count').textContent = `${exportResults().length} 条`;
}

function regenerateOutput() {
    clearTimeout(exportPreviewTimer);
    exportPreviewTimer = null;
    const results = exportResults();
    $('output-box').value = results.length ? formatResults($('format-template').value, results) : '';
    $('output-count').textContent = `${results.length} 行`;
    $('export-count').textContent = `${results.length} 条`;
}

function scheduleExportPreview() {
    refreshExportCount();
    clearTimeout(exportPreviewTimer);
    exportPreviewTimer = setTimeout(regenerateOutput, 140);
}

function persistSavedTemplates() {
    try {
        localStorage.setItem(SAVED_TEMPLATE_KEY, JSON.stringify(savedTemplates));
    } catch {
        toast('浏览器不允许保存模板');
    }
}

function renderSavedTemplates() {
    $('saved-templates').innerHTML = '<option value="">已保存模板</option>' + savedTemplates.map((item, index) =>
        `<option value="${index}">${escapeHTML(item.name)}</option>`).join('');
    $('btn-delete-template').disabled = !$('saved-templates').value;
}

function loadSavedTemplates() {
    try {
        const parsed = JSON.parse(localStorage.getItem(SAVED_TEMPLATE_KEY) || '[]');
        savedTemplates = Array.isArray(parsed)
            ? parsed.filter(item => item && typeof item.name === 'string' && typeof item.template === 'string')
            : [];
    } catch {
        savedTemplates = [];
    }
    renderSavedTemplates();
}

function bindExport() {
    const presets = $('format-presets');
    presets.innerHTML = PRESETS.map(item => `<option value="${item.template}">${item.name}</option>`).join('');
    presets.value = '{ip}:{port}#{country}';
    presets.addEventListener('change', () => {
        $('format-template').value = presets.value;
        regenerateOutput();
    });
    $('format-template').addEventListener('input', regenerateOutput);
    $('placeholder-help').innerHTML = '字段：' + placeholderNames().map(name => `<code data-ph="${name}">${name}</code>`).join('');
    $('placeholder-help').addEventListener('click', event => {
        const placeholder = event.target.dataset?.ph;
        if (!placeholder) return;
        $('format-template').value += placeholder;
        regenerateOutput();
    });
    document.querySelectorAll('input[name="export-scope"]').forEach(input =>
        input.addEventListener('change', regenerateOutput));
    loadSavedTemplates();
    $('btn-save-template').addEventListener('click', () => {
        const name = $('template-name').value.trim();
        const template = $('format-template').value.trim();
        if (!name || !template) { toast('请填写模板名称和内容'); return; }
        const existing = savedTemplates.find(item => item.name === name);
        if (existing) existing.template = template;
        else savedTemplates.push({ name, template });
        persistSavedTemplates();
        renderSavedTemplates();
        $('saved-templates').value = String(savedTemplates.findIndex(item => item.name === name));
        $('btn-delete-template').disabled = false;
        toast(existing ? '模板已更新' : '模板已保存');
    });
    $('saved-templates').addEventListener('change', () => {
        const rawIndex = $('saved-templates').value;
        const index = rawIndex === '' ? -1 : Number(rawIndex);
        const item = savedTemplates[index];
        $('btn-delete-template').disabled = !item;
        if (!item) return;
        $('template-name').value = item.name;
        $('format-template').value = item.template;
        regenerateOutput();
    });
    $('btn-delete-template').addEventListener('click', () => {
        const rawIndex = $('saved-templates').value;
        const index = rawIndex === '' ? -1 : Number(rawIndex);
        if (!savedTemplates[index]) return;
        savedTemplates.splice(index, 1);
        persistSavedTemplates();
        renderSavedTemplates();
        toast('模板已删除');
    });
    $('btn-copy').addEventListener('click', async () => {
        regenerateOutput();
        const text = $('output-box').value;
        if (!text) { toast('没有可复制的结果'); return; }
        await copyToClipboard(text);
        toast(`已复制 ${exportResults().length} 条 TXT`);
    });
    $('btn-download-txt').addEventListener('click', () => {
        regenerateOutput();
        const text = $('output-box').value;
        if (!text) { toast('没有可下载的结果'); return; }
        downloadAsText(text);
    });
    $('btn-download-csv').addEventListener('click', () => {
        const results = exportResults();
        if (!results.length) { toast('当前导出范围没有结果'); return; }
        const columns = CSV_COLUMNS.filter(column => visibleColumnKeys.includes(column.key));
        if (!columns.length) { toast('没有可导出的显示字段'); return; }
        downloadAsCSV(results, columns, { enableTLS: $('lat-tls').checked });
        toast(`已导出 ${results.length} 条 × ${columns.length} 列`);
    });
    regenerateOutput();
}

async function init() {
    bindModes();
    bindProxyInput();
    bindOfficialInput();
    bindResults();
    bindRulesAndRun();
    bindExport();
    bindEvents();
    renderRawSummary();
    renderCandidates();

    try {
        const config = await api.fetchConfig();
        defaults = config.defaults;
        $('app-version').textContent = config.version || 'dev';
        const status = $('res-status');
        status.textContent = `位置 ${config.locationCount} · ASN ${config.asnLoaded ? '就绪' : '未加载'}`;
        status.classList.add('ok');
        resetRules();
    } catch (error) {
        $('res-status').textContent = `后端不可用：${error.message}`;
        resetRules();
    }
}

init();
