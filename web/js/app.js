// app.js —— 主流程：候选准备 → 规则执行 → 结果整理 → 格式导出

import { getInputStats, smartFilter, parseFilterExpression, importCSVText } from './input.js';
import * as api from './api.js';
import { store, setMode, parseLines, targetToLine } from './store.js';
import { ResultTable, CSV_COLUMNS } from './table.js';
import { ALL_COLUMNS, TABLE_COLUMNS, GROUP_COLUMNS, DEFAULT_BADGE_THRESHOLDS, escapeHTML, setBadgeThresholds } from './columns.js';
import { createMultiSelect } from './multiselect.js';
import { PRESETS, placeholderNames } from './composer.js';
import { download, copyToClipboard, serialize as serializeExport } from './exporter.js';

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
let sourceInputText = '';
let showingFilteredInput = false;
let appConfig = null;
let quotaRuleSeq = 0;
const quotaRuleEditors = new Map();
let officialEstimateTimer = null;
let exportPreviewTimer = null;
let savedTemplates = [];
let customResults = null; // null = 尚未进入自定义，进入时默认用当前展示结果
let customTouched = false; // 清空或追加后，自定义列表由用户接管

const SAVED_TEMPLATE_KEY = 'iptest.savedTemplates.v1';

const DEFAULT_COLUMN_KEYS = TABLE_COLUMNS.filter(c => c.key !== '_sel').map(c => c.key);
const SELECTABLE_COLUMN_KEYS = ALL_COLUMNS
    .filter(column => column.key !== '_sel' && column.key !== 'enableTLS' && column.inCSV)
    .map(column => column.key);
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
    return sourceInputText.split(/\r?\n/).map(line => line.trim()).filter(Boolean);
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
        showingFilteredInput = false;
        $('ip-input').readOnly = false;
        if ($('ip-input').value !== sourceInputText) $('ip-input').value = sourceInputText;
        return;
    }
    const valid = parseFilterExpression(expression) !== null;
    $('filter-expr').style.borderColor = valid ? '' : 'var(--danger)';
    const selected = valid ? selectedRawLines().length : lines.length;
    const selectedLines = valid ? selectedRawLines() : lines;
    showingFilteredInput = valid;
    $('ip-input').readOnly = valid;
    $('ip-input').value = selectedLines.join('\n');
    $('filter-summary').textContent = valid
        ? `${filterMode === 'keep' ? '保留' : '排除'}匹配：当前将加入 ${selected}/${lines.length} 行`
        : '筛选表达式无效，将按全部输入处理';
}

function appendRawText(text) {
    const next = String(text || '').trim();
    if (!next) return;
    sourceInputText = sourceInputText.trim() ? `${sourceInputText.trim()}\n${next}` : next;
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
    $('ip-input').addEventListener('input', () => {
        if (!showingFilteredInput) sourceInputText = $('ip-input').value;
        renderRawSummary();
    });
    $('filter-expr').addEventListener('input', renderRawSummary);
    $('btn-filter-keep').addEventListener('click', () => { filterMode = 'keep'; renderRawSummary(); });
    $('btn-filter-remove').addEventListener('click', () => { filterMode = 'remove'; renderRawSummary(); });
    $('btn-filter-clear').addEventListener('click', () => {
        $('filter-expr').value = '';
        filterMode = 'keep';
        renderRawSummary();
    });
    document.getElementById('btn-filter-help').addEventListener('click', () => {
        const panel = document.getElementById('filter-help');
        panel.hidden = !panel.hidden;
    });
    $('btn-workspace-clear').addEventListener('click', () => {
        sourceInputText = '';
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
            appendRawText(file.name.toLowerCase().endsWith('.csv') ? importCSVText(text) : text);
            toast(`已加载 ${file.name}`);
        } catch (error) {
            toast(`读取文件失败：${error.message}`);
        }
        event.target.value = '';
    });

    $('btn-import-remote').addEventListener('click', async () => {
        const url = $('remote-url').value.trim();
        if (!url) { toast('请填写远程 TXT / CSV 地址'); return; }
        const button = $('btn-import-remote');
        button.disabled = true;
        button.textContent = '加载中…';
        try {
            const resp = await api.importRemote(url, { sampleMode: 'one', sampleN: 1 });
            const imported = resp.format === 'csv' ? importCSVText(resp.text || '') : (resp.text || '');
            appendRawText(imported || resp.targets.map(targetToLine).join('\n'));
            toast(`远程 ${resp.format === 'csv' ? 'CSV' : 'TXT'} 已加载到输入框`);
        } catch (error) {
            toast(error.message);
        } finally {
            button.disabled = false;
            button.textContent = '加载';
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

async function fetchRanges(refresh = false) {
    const button = $('btn-fetch-ranges');
    button.disabled = true;
    try {
        officialRanges = await api.fetchOfficialRanges(officialSettings().sampleN, { refresh });
        const source = { builtin: '内置兜底', cache: '本地缓存', remote: '官方接口' }[officialRanges.source] || officialRanges.source;
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
    $('btn-fetch-ranges').addEventListener('click', () => fetchRanges(false));
    $('btn-refresh-ranges').addEventListener('click', async () => {
        const button = $('btn-refresh-ranges');
        button.disabled = true;
        try { await fetchRanges(true); toast('官方网段已远程更新并写入本地缓存'); }
        finally { button.disabled = false; }
    });
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

function bindFlowNavigation() {
    const links = [...document.querySelectorAll('.flow-step')];
    const sections = links.map(link => document.querySelector(link.getAttribute('href'))).filter(Boolean);
    // 导航高亮只是增强效果；不支持 IntersectionObserver 的浏览器也必须能
    // 完成导入、设置和检测，不能让初始化在这里中断。
    if (!('IntersectionObserver' in window)) return;
    const observer = new IntersectionObserver(entries => {
        const visible = entries.filter(entry => entry.isIntersecting)
            .sort((a, b) => b.intersectionRatio - a.intersectionRatio)[0];
        if (!visible) return;
        links.forEach(link => link.classList.toggle('active', link.getAttribute('href') === `#${visible.target.id}`));
    }, { rootMargin: '-64px 0px -55% 0px', threshold: [0.1, 0.35, 0.6] });
    sections.forEach(section => observer.observe(section));
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
    const phase = progress.phase === 'speed' ? '测速' : progress.phase === 'pipeline' ? '延迟+测速' : '延迟检测';
    $('progress-label').textContent = `${phase} ${progress.completed}/${progress.total} · 符合 ${progress.validIPs}`;
}

async function startPipeline() {
    const targets = activeCandidates();
    if (!targets.length) { toast('候选区为空'); return; }
    table.clear();
    customResults = null;
    customTouched = false;
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
        $('progress-label').textContent = `${speedEnabled() ? '延迟+测速' : '延迟检测'} 0/${response.totalTargets}`;
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
    ['badge-latency-fast', 'badge-latency-mid', 'badge-speed-fast', 'badge-speed-mid'].forEach(id => {
        $(id).addEventListener('input', applyBadgeThresholdsFromUI);
    });
    $('lat-tls').addEventListener('change', () => {
        $('spd-tls').checked = $('lat-tls').checked;
        updateDefaultPortHint();
    });
    $('advanced-speed-url').addEventListener('input', () => { $('spd-url').value = $('advanced-speed-url').value; });
    $('rule-maxresults').addEventListener('input', () => { $('spd-maxresults').value = ruleMaxResults(); });
    document.getElementById('btn-reset-rules').addEventListener('click', () => { resetRules(); toast('已恢复推荐设置'); });
    $('btn-save-settings').addEventListener('click', saveLocalSettings);
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
            refreshQuotaEditors();
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
    const choices = SELECTABLE_COLUMN_KEYS.map(key => ALL_COLUMNS.find(column => column.key === key));
    $('column-options').innerHTML = choices.map(column => `
        <label class="checkbox"><input type="checkbox" data-key="${column.key}" ${visibleColumnKeys.includes(column.key) ? 'checked' : ''}> ${escapeHTML(column.label)}</label>`).join('');
    $('column-selected-count').textContent = `已选 ${visibleColumnKeys.length}/${choices.length}`;
}

function applyColumnsFromUI() {
    const keys = [...document.querySelectorAll('#column-options input:checked')].map(input => input.dataset.key);
    if (!keys.length) { toast('至少保留一个显示字段'); renderColumnOptions(); return; }
    visibleColumnKeys = keys;
    table.setColumns(keys);
    renderSortOptions();
    $('column-selected-count').textContent = `已选 ${keys.length}/${document.querySelectorAll('#column-options input').length}`;
    $('column-save-status').textContent = '字段有修改，尚未保存';
}

async function saveDisplayColumns() {
    try {
        await api.saveSettings(currentSettings());
        $('column-save-status').textContent = '显示字段已保存';
        toast('显示字段已保存到 data/settings.json');
    } catch (error) {
        $('column-save-status').textContent = '保存失败';
        toast(error.message);
    }
}

function updateCountryFilter() {
    // 国家已可通过关键词与组合规则筛选，不再维护重复的单选下拉。
}

function applyResultFilters() {
    table.setFilter($('result-filter').value);
    table.setFilters({
        maxLatency: parseFloat($('result-max-latency').value) || 0,
        minSpeed: parseFloat($('result-min-speed').value) || 0,
    });
    refreshButtons();
    refreshQuotaEditors();
    scheduleExportPreview();
}

function quotaItems(dimension) {
    return table.getGroupStats(dimension, { filtered: true }).map(item => ({
        value: String(item.name),
        label: item.emoji ? `${item.emoji} ${item.name}` : String(item.name),
        count: item.count,
    }));
}

function addQuotaRule(seed = {}) {
    const id = `quota-rule-${++quotaRuleSeq}`;
    const row = document.createElement('div');
    row.className = 'quota-rule';
    row.dataset.id = id;
    row.innerHTML = `<div class="quota-rule-head"><strong>规则 ${quotaRuleSeq}</strong><span class="hint">每个值取前</span><input class="quota-rule-limit" type="number" min="0" value="${Number(seed.limit) || ''}" placeholder="无限制"><span class="hint">个</span><button class="small quota-rule-add-condition" type="button">添加限制字段</button><button class="small quota-rule-remove" type="button">删除规则</button></div><div class="quota-conditions"></div>`;
    $('quota-rules').appendChild(row);
    const conditions = [];
    function addCondition(condition = {}) {
        const line = document.createElement('div');
        line.className = 'quota-condition';
        line.innerHTML = `<span class="quota-condition-role"></span><select>${GROUP_COLUMNS.map(item => `<option value="${item.key}">${item.label}</option>`).join('')}</select><span class="quota-rule-picker"></span><button class="small quota-condition-remove" type="button">删除</button>`;
        row.querySelector('.quota-conditions').appendChild(line);
        const dimension = line.querySelector('select');
        dimension.value = condition.field || condition.dimension || seed.dimension || 'country';
        const picker = createMultiSelect(line.querySelector('.quota-rule-picker'), { placeholder: '选择一个或多个值' });
        const refill = values => {
            const selected = (values || picker.getSelectedInOrder()).map(String);
            const items = quotaItems(dimension.value);
            const known = new Set(items.map(item => item.value));
            selected.filter(value => !known.has(value)).forEach(value => items.push({ value, label: value, count: 0 }));
            picker.setItems(items); picker.setSelected(selected);
        };
        refill(condition.values || seed.values || []);
        dimension.addEventListener('change', () => refill([]));
        line.querySelector('.quota-condition-remove').addEventListener('click', () => {
            picker.destroy();
            const idx = conditions.findIndex(item => item.line === line);
            if (idx >= 0) conditions.splice(idx, 1);
            line.remove();
            updateRoles();
        });
        conditions.push({ line, picker });
        updateRoles();
    }
    function updateRoles() {
        conditions.forEach((item, index) => {
            item.line.querySelector('.quota-condition-role').textContent = index === 0 ? '分组字段' : '限制字段';
            item.line.querySelector('.quota-condition-remove').disabled = index === 0 && conditions.length === 1;
        });
    }
    const initial = Array.isArray(seed.conditions) && seed.conditions.length ? seed.conditions : [{ field: seed.dimension || 'country', values: seed.values || [] }];
    initial.forEach(addCondition);
    row.querySelector('.quota-rule-add-condition').addEventListener('click', () => addCondition());
    row.querySelector('.quota-rule-remove').addEventListener('click', () => {
        conditions.forEach(item => item.picker.destroy());
        quotaRuleEditors.delete(id);
        row.remove();
    });
    quotaRuleEditors.set(id, { row, conditions });
}

function readQuotaRules() {
    return [...quotaRuleEditors.values()].map(({ row, conditions }) => ({
        conditions: conditions.map(({ line, picker }) => ({ field: line.querySelector('select').value, values: picker.getSelectedInOrder() })).filter(condition => condition.values.length),
        limit: Number(row.querySelector('.quota-rule-limit').value) || 0,
    })).filter(rule => rule.conditions.length);
}

function currentSettings() {
    return {
        rules: { latency: latencyOptions(), speed: speedOptions(), speedEnabled: speedEnabled(), maxResults: ruleMaxResults() },
        columns: [...visibleColumnKeys],
        displayRules: readQuotaRules(),
        exportFormat: exportFormat(),
        formatTemplate: $('format-template').value,
        savedTemplates: savedTemplates.map(item => ({ ...item })),
        exportScope: exportScope(),
        ui: { badgeThresholds: readBadgeThresholds() },
    };
}

function readBadgeThresholds() {
    return {
        latencyFastMs: Number($('badge-latency-fast').value) || 0,
        latencyMidMs: Number($('badge-latency-mid').value) || 0,
        speedFastKBs: Number($('badge-speed-fast').value) || 0,
        speedMidKBs: Number($('badge-speed-mid').value) || 0,
    };
}

function applyBadgeThresholdsFromUI() {
    setBadgeThresholds(readBadgeThresholds());
    table?.render();
    scheduleExportPreview();
}

function fillBadgeThresholdFields(settings = {}) {
    const saved = settings.ui?.badgeThresholds || {};
    $('badge-latency-fast').value = Number(saved.latencyFastMs) > 0 ? saved.latencyFastMs : DEFAULT_BADGE_THRESHOLDS.latencyFastMs;
    $('badge-latency-mid').value = Number(saved.latencyMidMs) > 0 ? saved.latencyMidMs : DEFAULT_BADGE_THRESHOLDS.latencyMidMs;
    $('badge-speed-fast').value = Number(saved.speedFastKBs) > 0 ? saved.speedFastKBs : DEFAULT_BADGE_THRESHOLDS.speedFastKBs;
    $('badge-speed-mid').value = Number(saved.speedMidKBs) > 0 ? saved.speedMidKBs : DEFAULT_BADGE_THRESHOLDS.speedMidKBs;
    setBadgeThresholds(readBadgeThresholds());
}

function fillConfigFields(config) {
    appConfig = config || {};
    const sources = appConfig.sources || {};
    $('advanced-speed-url').value = appConfig.speedTestURL || '';
    $('advanced-trace-url').value = appConfig.traceURL || '';
    $('advanced-ips-url').value = appConfig.ipsTypeURL || '';
    $('advanced-location-sources').value = (sources.locations || []).join('\n');
    $('advanced-asn-sources').value = (sources.asnDatabase || []).join('\n');
    $('advanced-official-sources').value = (sources.officialRanges || []).join('\n');
}

function applySavedSettings(settings = {}) {
    const rules = settings.rules || {};
    const lat = rules.latency || {};
    const spd = rules.speed || {};
    if (lat.maxConcurrency) $('lat-concurrency').value = lat.maxConcurrency;
    if (lat.timeoutMs) $('lat-timeout').value = lat.timeoutMs;
    $('lat-maxlatency').value = Number(lat.maxLatencyMs) > 0 ? lat.maxLatencyMs : '';
    if (lat.enableTLS != null) $('lat-tls').checked = lat.enableTLS;
    if (lat.enableIPAPI != null) $('lat-ipapi').checked = lat.enableIPAPI;
    if (spd.maxConcurrency) $('spd-concurrency').value = spd.maxConcurrency;
    if (spd.durationSec) $('spd-duration').value = spd.durationSec;
    $('spd-minspeed').value = Number(spd.minSpeedKBs) > 0 ? spd.minSpeedKBs : '';
    if (rules.speedEnabled != null) $('spd-enable').checked = rules.speedEnabled;
    $('rule-maxresults').value = Number(rules.maxResults) > 0 ? rules.maxResults : '';
    if (settings.columns?.length) {
        const savedColumns = [...new Set(settings.columns)].filter(key => SELECTABLE_COLUMN_KEYS.includes(key));
        visibleColumnKeys = savedColumns.length ? savedColumns : [...DEFAULT_COLUMN_KEYS];
        renderColumnOptions(); table.setColumns(visibleColumnKeys); renderSortOptions();
    }
    if (settings.formatTemplate) $('format-template').value = settings.formatTemplate;
    if (settings.exportFormat === 'txt' || settings.exportFormat === 'csv') {
        $('export-format').value = settings.exportFormat;
    }
    if (Array.isArray(settings.savedTemplates)) {
        savedTemplates = settings.savedTemplates
            .filter(item => item && typeof item.name === 'string' && typeof item.template === 'string');
    }
    renderTemplateOptions();
    fillBadgeThresholdFields(settings);
    const legacyScope = { all: 'direct', visible: 'rules', selected: 'custom' }[settings.exportScope] || settings.exportScope;
    if (['direct', 'rules', 'custom'].includes(legacyScope)) {
        const scope = document.querySelector(`input[name="export-scope"][value="${legacyScope}"]`);
        if (scope) scope.checked = true;
    }
    clearQuotaEditors();
    const displayRules = settings.displayRules || settings.quotaRules;
    (displayRules?.length ? displayRules : [{}]).forEach(addQuotaRule);
    applySpeedEnabled(); updateDefaultPortHint(); regenerateOutput();
}

async function saveLocalSettings() {
    const cfg = {
        ...(appConfig || {}),
        traceURL: $('advanced-trace-url').value.trim(),
        ipsTypeURL: $('advanced-ips-url').value.trim(),
        speedTestURL: $('advanced-speed-url').value.trim(),
        sources: {
            locations: $('advanced-location-sources').value.split(/\r?\n/).map(v => v.trim()).filter(Boolean),
            asnDatabase: $('advanced-asn-sources').value.split(/\r?\n/).map(v => v.trim()).filter(Boolean),
            officialRanges: $('advanced-official-sources').value.split(/\r?\n/).map(v => v.trim()).filter(Boolean),
        },
    };
    try {
        const response = await api.saveConfig(cfg);
        appConfig = response.config;
        await api.saveSettings(currentSettings());
        $('settings-status').textContent = '已保存；数据源、Trace 与 IPS 地址修改重启后完全生效';
        toast('设置已保存到 data 目录');
    } catch (error) { toast(error.message); }
}

function clearQuotaEditors() {
    for (const { conditions } of quotaRuleEditors.values()) conditions.forEach(item => item.picker.destroy());
    quotaRuleEditors.clear();
    $('quota-rules').innerHTML = '';
}

function refreshQuotaEditors() {
    for (const { conditions } of quotaRuleEditors.values()) {
        conditions.forEach(({ line, picker }) => {
            const selected = picker.getSelectedInOrder();
            const items = quotaItems(line.querySelector('select').value);
            const known = new Set(items.map(item => item.value));
            selected.filter(value => !known.has(value)).forEach(value => items.push({ value, label: value, count: 0 }));
            picker.setItems(items); picker.setSelected(selected);
        });
    }
}

function bindQuotaPanel() {
    $('btn-quota-add-rule').addEventListener('click', () => addQuotaRule());
    $('btn-quota-toggle').addEventListener('click', () => {
        const box = $('quota-box');
        const open = !box.classList.contains('active');
        if (open && !quotaRuleEditors.size) addQuotaRule();
        box.classList.toggle('active', open);
    });
    $('btn-quota-apply').addEventListener('click', () => {
        const rules = readQuotaRules();
        const shown = table.applyDisplayRules(rules);
        toast(rules.length ? `已应用 ${rules.length} 条规则，当前展示 ${shown} 条` : '请至少选择一条规则的值');
        refreshButtons();
        scheduleExportPreview();
    });
    $('btn-quota-clear').addEventListener('click', () => {
        table.clearDisplayRules();
        clearQuotaEditors();
        addQuotaRule();
        refreshButtons();
        scheduleExportPreview();
    });
}

function refreshButtons() {
    if (!table) return;
    const running = currentTaskId !== null;
    $('btn-start-speed').disabled = running || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = running || table.getAllResults().length === 0;
}

function bindResults() {
    table = new ResultTable($('result-table-container'));
    renderColumnOptions();
    renderSortOptions();
    bindQuotaPanel();

    $('result-filter').addEventListener('input', applyResultFilters);
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

    $('btn-column-toggle').addEventListener('click', () => {
        const box = $('column-box');
        const open = !box.classList.contains('active');
        box.classList.toggle('active', open);
        box.open = open;
    });
    $('column-box').addEventListener('toggle', () => {
        if (!$('column-box').open) $('column-box').classList.remove('active');
    });
    $('column-options').addEventListener('change', applyColumnsFromUI);
    $('btn-column-all').addEventListener('click', () => {
        document.querySelectorAll('#column-options input').forEach(input => { input.checked = true; });
        applyColumnsFromUI();
    });
    $('btn-column-default').addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        visibleColumnKeys = [...DEFAULT_COLUMN_KEYS];
        renderColumnOptions();
        table.setColumns(visibleColumnKeys);
        renderSortOptions();
        $('column-save-status').textContent = '已恢复默认，尚未保存';
    });
    $('btn-column-save').addEventListener('click', saveDisplayColumns);
}

function exportScope() {
    return document.querySelector('input[name="export-scope"]:checked')?.value || 'direct';
}

function exportFormat() {
    return $('export-format')?.value === 'csv' ? 'csv' : 'txt';
}

function exportResults() {
    if (!table) return [];
    if (exportScope() === 'rules') return table.getAllResults();
    if (exportScope() === 'custom') {
        if (!customTouched) return table.getAllResults();
        if (customResults === null) customResults = [];
        return customResults;
    }
    return table.getResults();
}

function currentExportContent() {
    const results = exportResults();
    if (!results.length) return '';
    if (exportFormat() === 'csv') {
        const columns = CSV_COLUMNS.filter(column => visibleColumnKeys.includes(column.key));
        if (!columns.length) return '';
        return serializeExport(results, 'csv', { columns, enableTLS: $('lat-tls').checked });
    }
    return serializeExport(results, 'txt', { template: $('format-template').value });
}

function updateCustomExportUI() {
    const custom = exportScope() === 'custom';
    $('custom-export-actions').hidden = !custom;
    const count = custom && !customTouched
        ? (table?.getAllResults().length || 0)
        : (customResults?.length || 0);
    $('custom-count').textContent = custom ? `当前 ${count} 条` : '';
}

function regenerateOutput() {
    clearTimeout(exportPreviewTimer);
    exportPreviewTimer = null;
    const results = exportResults();
    $('output-box').value = currentExportContent();
    $('output-count').textContent = `${results.length} 条`;
    $('output-title').textContent = `${exportFormat() === 'csv' ? 'CSV' : 'TXT'} 预览`;
    updateCustomExportUI();
}

function scheduleExportPreview() {
    clearTimeout(exportPreviewTimer);
    exportPreviewTimer = setTimeout(regenerateOutput, 140);
}

function appendSelectedToCustom() {
    if (!table) return;
    const selected = table.getSelectedResultsInDisplayOrder();
    if (!selected.length) { toast('请先勾选要追加的结果'); return; }
    if (!customTouched) {
        customTouched = true;
        customResults = table.getAllResults().map(r => r);
    }
    const seen = new Set(customResults.map(keyOf));
    let added = 0;
    let duplicates = 0;
    for (const result of selected) {
        const key = keyOf(result);
        if (seen.has(key)) { duplicates++; continue; }
        seen.add(key);
        customResults.push(result);
        added++;
    }
    regenerateOutput();
    toast(added ? `已追加 ${added} 条${duplicates ? `，跳过重复 ${duplicates} 条` : ''}` : '没有新增结果（已在自定义列表中）');
}

function clearCustomResults() {
    customTouched = true;
    customResults = [];
    regenerateOutput();
    toast('已清空自定义导出列表');
}

async function persistSavedTemplates() {
    try {
        // 保留一份旧版浏览器缓存，便于旧版本回退；主存储已经迁移到 settings.json。
        localStorage.setItem(SAVED_TEMPLATE_KEY, JSON.stringify(savedTemplates));
    } catch {
        // 本地文件持久化不依赖浏览器缓存。
    }
    await api.saveSettings(currentSettings());
}

function renderTemplateOptions(selected = '') {
    const format = exportFormat();
    const txtEditor = $('txt-template-editor');
    const csvHint = $('csv-template-hint');
    if (format === 'csv') {
        const columns = CSV_COLUMNS.filter(column => visibleColumnKeys.includes(column.key));
        $('format-presets').innerHTML = `<option value="csv:visible">当前展示字段（${columns.length} 列）</option>`;
        $('format-presets').value = 'csv:visible';
        txtEditor.hidden = true;
        csvHint.hidden = false;
        $('csv-column-label').textContent = columns.map(c => c.label).join('、') || '未选择字段';
        $('btn-delete-template').disabled = true;
        return;
    }
    txtEditor.hidden = false;
    csvHint.hidden = true;
    const presetOptions = PRESETS.map((item, index) =>
        `<option value="preset:${index}">${escapeHTML(item.name)}</option>`).join('');
    const savedOptions = savedTemplates.map((item, index) =>
        `<option value="saved:${index}">${escapeHTML(item.name)}</option>`).join('');
    $('format-presets').innerHTML = `<optgroup label="内置模板">${presetOptions}</optgroup>`
        + (savedOptions ? `<optgroup label="我的模板">${savedOptions}</optgroup>` : '');
    $('format-presets').value = selected || 'preset:0';
    $('btn-delete-template').disabled = !$('format-presets').value.startsWith('saved:');
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
    renderTemplateOptions();
}

function bindExport() {
    const templates = $('format-presets');
    $('export-format').addEventListener('change', () => {
        renderTemplateOptions();
        regenerateOutput();
    });
    templates.addEventListener('change', () => {
        if (exportFormat() === 'csv') return;
        const [type, rawIndex] = templates.value.split(':');
        const index = Number(rawIndex);
        const item = type === 'saved' ? savedTemplates[index] : PRESETS[index];
        if (!item) return;
        $('template-name').value = type === 'saved' ? item.name : '';
        $('format-template').value = item.template;
        $('btn-delete-template').disabled = type !== 'saved';
        regenerateOutput();
    });
    $('format-template').addEventListener('input', regenerateOutput);
    const placeholders = placeholderNames();
    $('placeholder-count').textContent = `（${placeholders.length} 个）`;
    $('placeholder-help').innerHTML = placeholders.map(name => `<code data-ph="${name}">${name}</code>`).join('');
    $('placeholder-help').addEventListener('click', event => {
        const placeholder = event.target.dataset?.ph;
        if (!placeholder) return;
        $('format-template').value += placeholder;
        regenerateOutput();
    });
    document.querySelectorAll('input[name="export-scope"]').forEach(input =>
        input.addEventListener('change', () => {
            if (!input.checked) return;
            regenerateOutput();
        }));
    loadSavedTemplates();
    $('btn-custom-append').addEventListener('click', appendSelectedToCustom);
    $('btn-custom-clear').addEventListener('click', clearCustomResults);
    $('btn-save-template').addEventListener('click', async () => {
        const name = $('template-name').value.trim();
        const template = $('format-template').value.trim();
        if (!name || !template) { toast('请填写模板名称和内容'); return; }
        const existing = savedTemplates.find(item => item.name === name);
        if (existing) existing.template = template;
        else savedTemplates.push({ name, template });
        try {
            await persistSavedTemplates();
        } catch (error) {
            toast(`保存模板失败：${error.message}`);
            return;
        }
        const selected = `saved:${savedTemplates.findIndex(item => item.name === name)}`;
        renderTemplateOptions(selected);
        toast(existing ? '模板已更新' : '模板已保存');
    });
    $('btn-delete-template').addEventListener('click', async () => {
        const [type, rawIndex] = $('format-presets').value.split(':');
        const index = type === 'saved' ? Number(rawIndex) : -1;
        if (!savedTemplates[index]) return;
        savedTemplates.splice(index, 1);
        try {
            await persistSavedTemplates();
        } catch (error) {
            toast(`删除模板失败：${error.message}`);
            return;
        }
        renderTemplateOptions();
        $('template-name').value = '';
        $('format-template').value = PRESETS[0].template;
        regenerateOutput();
        toast('模板已删除');
    });
    $('btn-copy').addEventListener('click', async () => {
        regenerateOutput();
        const text = $('output-box').value;
        if (!text) { toast('没有可复制的结果'); return; }
        await copyToClipboard(text);
        toast(`已复制 ${exportResults().length} 条${exportFormat() === 'csv' ? ' CSV' : ' TXT'}`);
    });
    $('btn-download').addEventListener('click', () => {
        regenerateOutput();
        const text = $('output-box').value;
        if (!text) { toast('没有可下载的结果'); return; }
        const results = exportResults();
        if (exportFormat() === 'csv') {
            const columns = CSV_COLUMNS.filter(column => visibleColumnKeys.includes(column.key));
            download('\uFEFF' + text, 'iptest-result.csv', 'text/csv;charset=utf-8');
            toast(`已下载 ${results.length} 条 × ${columns.length} 列 CSV`);
        } else {
            download(text, 'iptest-result.txt', 'text/plain;charset=utf-8');
            toast(`已下载 ${results.length} 条 TXT`);
        }
    });
    regenerateOutput();
}

async function init() {
    bindFlowNavigation();
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
        $('app-version').textContent = `版本号：${config.version || 'dev'}`;
        const status = $('res-status');
        status.textContent = `位置 ${config.locationCount} · ASN ${config.asnLoaded ? '就绪' : '未加载'}`;
        status.classList.add('ok');
        resetRules();
        fillConfigFields(config.config);
        applySavedSettings(config.settings);
    } catch (error) {
        $('res-status').textContent = `后端不可用：${error.message}`;
        resetRules();
    }
}

init();
