// app.js —— 主流程：候选准备 → 规则执行 → 结果整理 → 格式导出

import { getInputStats, smartFilter, parseFilterExpression, importCSVText } from './input.js';
import * as api from './api.js';
import { store, setMode, parseLines, targetToLine } from './store.js';
import { ResultTable, CSV_COLUMNS } from './table.js';
import { ALL_COLUMNS, TABLE_COLUMNS, GROUP_COLUMNS, DEFAULT_BADGE_THRESHOLDS, csvValue, escapeHTML, normalizeBadgeThresholds, setBadgeThresholds } from './columns.js';
import { createMultiSelect } from './multiselect.js';
import { PRESETS, placeholderNames } from './composer.js';
import { download, copyToClipboard, serialize as serializeExport } from './exporter.js';
import { boundedNumber } from './validation.js';
import { initTasks } from './tasks.js';
import { initLibrary } from './library.js';

const $ = id => document.getElementById(id);
const keyOf = item => `${item.ip}|${item.port || 0}`;

let currentTaskId = null;
let currentTaskType = null; // pipeline | speed
let eventSource = null;
let eventStreamDisconnected = false;
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
let customResults = []; // 自定义导出列表，默认空，仅手动加入或勾选追加
let customColumnKeys = CSV_COLUMNS.map(column => column.key); // 自定义 CSV 字段，默认全部
let officialRangesLoading = false;

const SAVED_TEMPLATE_KEY = 'iptest.savedTemplates.v1';

const DEFAULT_COLUMN_KEYS = TABLE_COLUMNS.filter(c => c.key !== '_sel').map(c => c.key);
const SELECTABLE_COLUMN_KEYS = ALL_COLUMNS
    .filter(column => column.key !== '_sel' && column.key !== 'enableTLS' && column.inCSV)
    .map(column => column.key);
let visibleColumnKeys = [...DEFAULT_COLUMN_KEYS];
let templateEditorOpen = false; // 导出弹窗里「＋模板设置」的展开状态

let toastTimer = null;
let tasksPage = null;
let libPage = null;
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

    $('import-source').addEventListener('change', () => {
        const source = $('import-source').value;
        $('import-remote-url').hidden = source !== 'remote';
        $('import-library-select').hidden = source !== 'library';
        if (source === 'file') $('file-input').click();
        if (source === 'paste') $('ip-input').focus();
    });
    $('btn-import-remote').addEventListener('click', async () => {
        const source = $('import-source').value;
        if (source === 'file') { $('file-input').click(); return; }
        if (source === 'library') { await importFromLibrary(); return; }
        const url = $('import-remote-url').value.trim();
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
            button.textContent = '导入';
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
    if (officialRangesLoading) return;
    officialRangesLoading = true;
    $('ranges-status').textContent = refresh ? '正在更新本地缓存…' : '正在加载官方网段…';
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
        officialRangesLoading = false;
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
    $('btn-refresh-ranges').addEventListener('click', async () => {
        const button = $('btn-refresh-ranges');
        button.disabled = true;
        try {
            await fetchRanges(true);
            if (officialRanges?.source === 'remote') toast('官方网段已更新并写入本地缓存');
        } finally { button.disabled = false; }
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
    const tabs = [...document.querySelectorAll('.mode-tab')];
    const activateTab = tab => {
        setMode(tab.dataset.mode);
        tabs.forEach(item => {
            const active = item.dataset.mode === store.mode;
            item.classList.toggle('active', active);
            item.setAttribute('aria-selected', String(active));
            item.tabIndex = active ? 0 : -1;
        });
        $('source-proxy').hidden = store.mode !== 'proxy';
        $('source-official').hidden = store.mode !== 'official';
        renderCandidates();
        if (store.mode === 'official' && !officialRanges) fetchRanges(false);
    };
    $('mode-tabs').addEventListener('click', event => {
        const tab = event.target.closest('.mode-tab');
        if (tab) activateTab(tab);
    });
    $('mode-tabs').addEventListener('keydown', event => {
        const tab = event.target.closest('.mode-tab');
        if (!tab) return;
        const current = tabs.indexOf(tab);
        let next = current;
        if (event.key === 'ArrowRight') next = (current + 1) % tabs.length;
        else if (event.key === 'ArrowLeft') next = (current - 1 + tabs.length) % tabs.length;
        else if (event.key === 'Home') next = 0;
        else if (event.key === 'End') next = tabs.length - 1;
        else return;
        event.preventDefault();
        tabs[next].focus();
        activateTab(tabs[next]);
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
    const normalized = readNumberField('rule-maxresults', { min: 0, integer: true, optional: true });
    return Math.max(0, normalized ?? 0);
}

function speedEnabled() { return $('spd-enable').checked; }

function readNumberField(id, { min = -Infinity, max = Infinity, integer = false, optional = false } = {}) {
    const field = $(id);
    const result = boundedNumber(field.value, { min, max, integer, emptyValue: 0 });
    if (!result.empty) field.value = result.value;
    return result.empty && optional ? undefined : result.value;
}

function normalizeRuleFields({ notify = false } = {}) {
    const rules = [
        ['lat-concurrency', { min: 1, max: 1000, integer: true }],
        ['lat-timeout', { min: 200, max: 10000, integer: true }],
        ['lat-maxlatency', { min: 0, max: 10000, integer: true, optional: true }],
        ['spd-concurrency', { min: 1, max: 100, integer: true }],
        ['spd-duration', { min: 1, max: 30, integer: true }],
        ['spd-minspeed', { min: 0, optional: true }],
        ['rule-maxresults', { min: 0, integer: true, optional: true }],
    ];
    let changed = false;
    rules.forEach(([id, options]) => {
        const field = $(id);
        const result = boundedNumber(field.value, { ...options, emptyValue: 0 });
        if (!result.empty && result.changed) changed = true;
        if (!result.empty) field.value = result.value;
    });
    if (normalizeBadgeFields()) changed = true;
    if (notify && changed) toast('已自动修正超出范围的检测参数');
    return changed;
}

function latencyOptions() {
    normalizeRuleFields();
    return {
        maxConcurrency: readNumberField('lat-concurrency', { min: 1, max: 1000, integer: true, optional: true }),
        timeoutMs: readNumberField('lat-timeout', { min: 200, max: 10000, integer: true, optional: true }),
        maxLatencyMs: readNumberField('lat-maxlatency', { min: 0, max: 10000, integer: true, optional: true }) || 0,
        // 启用速度规则时，统一数量限制只在最终测速阶段生效。
        maxResults: speedEnabled() ? 0 : ruleMaxResults(),
        enableTLS: $('lat-tls').checked,
        enableIPAPI: $('lat-ipapi').checked,
    };
}

function speedOptions() {
    normalizeRuleFields();
    return {
        maxConcurrency: readNumberField('spd-concurrency', { min: 1, max: 100, integer: true, optional: true }),
        durationSec: readNumberField('spd-duration', { min: 1, max: 30, integer: true, optional: true }),
        minSpeedKBs: readNumberField('spd-minspeed', { min: 0, optional: true }) || 0,
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
    $('progress-wrap').setAttribute('aria-valuenow', String(percent));
    $('progress-wrap').setAttribute('aria-valuetext', `${percent}%`);
    const phase = progress.phase === 'speed' ? '测速' : progress.phase === 'pipeline' ? '延迟+测速' : '延迟检测';
    $('progress-label').textContent = `${phase} ${progress.completed}/${progress.total} · 符合 ${progress.validIPs}`;
}

async function startPipeline() {
    normalizeRuleFields({ notify: true });
    const targets = activeCandidates();
    if (!targets.length) { toast('候选区为空'); return; }
    table.clear();
    customResults = [];
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
    normalizeRuleFields({ notify: true });
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

// 导入到 IP 库：与复制/下载平级，按当前导出范围（全部/当前规则/自定义）取结果
async function importResultsToLib() {
    const results = exportResults();
    if (!results.length) { toast('当前导出范围没有结果'); return; }
    const lib = $('lib-target-select').value || '';
    const libName = $('lib-target-select').selectedOptions[0]?.textContent || '默认库';
    try {
        const response = await api.importAutoLibrary({ lib, results });
        toast(`已导入「${libName}」：新增 ${response.added} 条，更新 ${response.updated} 条（共 ${response.total} 条）`);
        libPage?.refresh?.();
    } catch (error) {
        toast(`导入失败：${error.message}`);
    }
}

async function refreshLibraryTargets() {
    try {
        const data = await api.fetchLibraries();
        const list = data.libraries || [];
        const sel = $('lib-target-select');
        sel.innerHTML = list.map(l => `<option value="${escapeHTML(l.id)}">${escapeHTML(l.name)}</option>`).join('');
    } catch { /* 忽略 */ }
}

function bindRulesAndRun() {
    $('spd-enable').addEventListener('change', applySpeedEnabled);
    ['badge-latency-green-end', 'badge-latency-yellow-end', 'badge-speed-red-end', 'badge-speed-yellow-end'].forEach(id => {
        $(id).addEventListener('input', previewBadgeThresholdsFromUI);
        $(id).addEventListener('change', applyBadgeThresholdsFromUI);
    });
    ['lat-concurrency', 'lat-timeout', 'lat-maxlatency', 'spd-concurrency', 'spd-duration', 'spd-minspeed', 'rule-maxresults']
        .forEach(id => $(id).addEventListener('change', () => normalizeRuleFields({ notify: true })));
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

async function reconcileTaskStatus({ reconnected = false } = {}) {
    try {
        const status = await api.fetchTaskStatus();
        if (status.status === 'running' && status.taskId) {
            const changedTask = currentTaskId !== status.taskId;
            currentTaskId = status.taskId;
            setRunning(true, status.taskId.startsWith('spd-') ? 'speed' : 'pipeline');
            if (changedTask) $('progress-label').textContent = '后台任务运行中';
            if (reconnected) toast('事件流已恢复连接');
            return;
        }
        if (currentTaskId) {
            setRunning(false);
            $('progress-label').textContent = reconnected ? '任务已在断线期间结束' : '任务已结束';
            refreshButtons();
            regenerateOutput();
            if (reconnected) toast('事件流已恢复，后台任务已结束');
        }
    } catch (error) {
        if (reconnected) toast(`事件流已恢复，但任务状态同步失败：${error.message}`);
    }
}

function bindEvents() {
    eventSource = api.subscribeEvents({
        onOpen: () => {
            const reconnected = eventStreamDisconnected;
            eventStreamDisconnected = false;
            reconcileTaskStatus({ reconnected });
        },
        onDisconnect: () => {
            if (eventStreamDisconnected) return;
            eventStreamDisconnected = true;
            if (currentTaskId) {
                $('progress-label').textContent = '事件流中断，正在重连…';
                toast('事件流连接中断，后台任务继续运行');
            }
        },
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
        onAuto: message => { tasksPage?.onAuto(message); sideLogFromAuto(message); },
        onDone: (message, reason) => {
            if (tasksPage?.isAutoRunning()) { tasksPage.onDone(message, reason); return; }
            setRunning(false);
            $('progress-label').textContent = reason === 'limit' ? '已达到最大数量' : reason === 'stopped' ? '已停止' : '已完成';
            $('progress-pct').textContent = reason === 'stopped' ? $('progress-pct').textContent : '100%';
            if (reason !== 'stopped') {
                $('progress-wrap').setAttribute('aria-valuenow', '100');
                $('progress-wrap').setAttribute('aria-valuetext', '100%');
            }
            $('result-count').textContent = `（${table.results.length} 个有效节点）`;
            toast(message || '任务完成');
            refreshButtons();
            regenerateOutput();
        },
        onError: message => {
            if (tasksPage?.isAutoRunning()) { tasksPage.onDone(message, 'stopped'); return; }
            if (!currentTaskId) return;
            setRunning(false);
            $('progress-label').textContent = '任务出错';
            toast(message || '任务出错');
        },
    });
    window.addEventListener('beforeunload', () => eventSource?.close());
}

// SSE auto 事件转成侧边栏日志行
function sideLogFromAuto(message) {
    if (!message) return;
    let p;
    try { p = JSON.parse(message); } catch { return; }
    if (p.stage === 'report' && p.report) {
        const r = p.report;
        const shortage = (r.shortages || []).length ? `，缺口 ${r.shortages.length} 项` : '';
        sideLog(`任务「${r.subscription || ''}」完成：输出 ${r.totalLines ?? 0} 行，移除失效 ${r.removedDead ?? 0}，回写 ${(r.groups || []).reduce((s, g) => s + (g.updated || 0), 0)}${shortage}`, 'ok');
        return;
    }
    if (p.log) sideLog(`[${p.group || '维护'}] ${p.log}`);
}

function selectedColumns() {
    return visibleColumnKeys.map(key => ALL_COLUMNS.find(column => column.key === key)).filter(Boolean);
}

function renderSortOptions() {
    const sortable = selectedColumns().filter(column => column.sortable);
    $('sort-key').innerHTML = '<option value="">默认排序</option>' + sortable.map(column => `<option value="${column.key}">${escapeHTML(column.label)}</option>`).join('');
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
    renderTemplateOptions();
    scheduleExportPreview();
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
        customColumns: [...customColumnKeys],
        ui: { badgeThresholds: readBadgeThresholds() },
    };
}

function readBadgeThresholds() {
    return {
        latencyGreenEndMs: Number($('badge-latency-green-end').value) || 0,
        latencyYellowEndMs: Number($('badge-latency-yellow-end').value) || 0,
        speedRedEndKBs: Number($('badge-speed-red-end').value) || 0,
        speedYellowEndKBs: Number($('badge-speed-yellow-end').value) || 0,
    };
}

function updateBadgeRangeLabels(thresholds) {
    $('badge-latency-yellow-start').textContent = `${thresholds.latencyGreenEndMs} ms`;
    $('badge-latency-red-range').textContent = `${thresholds.latencyYellowEndMs} ms`;
    $('badge-speed-yellow-start').textContent = `${thresholds.speedRedEndKBs} kB/s`;
    $('badge-speed-green-range').textContent = `${thresholds.speedYellowEndKBs} kB/s`;
}

function previewBadgeThresholdsFromUI() {
    const values = readBadgeThresholds();
    const fallback = normalizeBadgeThresholds(values);
    updateBadgeRangeLabels({
        latencyGreenEndMs: values.latencyGreenEndMs || fallback.latencyGreenEndMs,
        latencyYellowEndMs: values.latencyYellowEndMs || fallback.latencyYellowEndMs,
        speedRedEndKBs: values.speedRedEndKBs || fallback.speedRedEndKBs,
        speedYellowEndKBs: values.speedYellowEndKBs || fallback.speedYellowEndKBs,
    });
}

function normalizeBadgeFields() {
    const normalized = normalizeBadgeThresholds(readBadgeThresholds());
    const fields = [
        ['badge-latency-green-end', normalized.latencyGreenEndMs],
        ['badge-latency-yellow-end', normalized.latencyYellowEndMs],
        ['badge-speed-red-end', normalized.speedRedEndKBs],
        ['badge-speed-yellow-end', normalized.speedYellowEndKBs],
    ];
    let changed = false;
    fields.forEach(([id, value]) => {
        if (String($(id).value) !== String(value)) changed = true;
        $(id).value = value;
    });
    updateBadgeRangeLabels(normalized);
    return changed;
}

function applyBadgeThresholdsFromUI() {
    normalizeBadgeFields();
    setBadgeThresholds(readBadgeThresholds());
    table?.render();
    scheduleExportPreview();
}

function fillBadgeThresholdFields(settings = {}) {
    const normalized = normalizeBadgeThresholds(settings.ui?.badgeThresholds || {});
    $('badge-latency-green-end').value = normalized.latencyGreenEndMs;
    $('badge-latency-yellow-end').value = normalized.latencyYellowEndMs;
    $('badge-speed-red-end').value = normalized.speedRedEndKBs;
    $('badge-speed-yellow-end').value = normalized.speedYellowEndKBs;
    updateBadgeRangeLabels(normalized);
    setBadgeThresholds(normalized);
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
    if (Array.isArray(settings.customColumns)) {
        const savedCustom = [...new Set(settings.customColumns)].filter(key => CSV_COLUMNS.some(column => column.key === key));
        customColumnKeys = savedCustom.length ? savedCustom : CSV_COLUMNS.map(column => column.key);
        renderCustomFieldOptions();
    }
    if (settings.formatTemplate) $('format-template').value = settings.formatTemplate;
    if (settings.exportFormat === 'txt' || settings.exportFormat === 'csv') {
        $('export-format').value = settings.exportFormat;
    }
    $('log-enable').checked = settings.debugLog === true;
    if (Array.isArray(settings.savedTemplates)) {
        savedTemplates = settings.savedTemplates
            .filter(item => item && typeof item.name === 'string' && typeof item.template === 'string');
    }
    fillBadgeThresholdFields(settings);
    const legacyScope = { all: 'direct', visible: 'rules', selected: 'custom' }[settings.exportScope] || settings.exportScope;
    if (['direct', 'rules', 'custom'].includes(legacyScope)) {
        const scope = document.querySelector(`input[name="export-scope"][value="${legacyScope}"]`);
        if (scope) scope.checked = true;
    }
    renderTemplateOptions();
    clearQuotaEditors();
    const displayRules = settings.displayRules || settings.quotaRules;
    (displayRules?.length ? displayRules : [{}]).forEach(addQuotaRule);
    applySpeedEnabled(); updateDefaultPortHint(); regenerateOutput();
}

async function saveLocalSettings() {
    normalizeRuleFields({ notify: true });
    applyBadgeThresholdsFromUI();
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
        $('btn-quota-toggle').classList.toggle('active', open);
        $('btn-quota-toggle').setAttribute('aria-expanded', String(open));
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

function syncColumnToggle() {
    const open = $('column-box').classList.contains('active');
    $('btn-column-toggle').classList.toggle('active', open);
    $('btn-column-toggle').setAttribute('aria-expanded', String(open));
}

function refreshButtons() {
    if (!table) return;
    const running = currentTaskId !== null;
    $('btn-start-speed').disabled = running || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = running || table.getAllResults().length === 0;
    $('btn-custom-append').disabled = running || table.getSelectedResultsInDisplayOrder().length === 0;
}

function bindResults() {
    table = new ResultTable($('result-table-container'));
    renderColumnOptions();
    renderSortOptions();
    bindQuotaPanel();

    $('result-filter').addEventListener('input', applyResultFilters);
    $('result-max-latency').addEventListener('input', applyResultFilters);
    $('result-min-speed').addEventListener('input', applyResultFilters);
    $('sort-key').addEventListener('change', () => {
        const key = $('sort-key').value;
        if (!key) table.clearSort(); else table.setSort(key, table.sortAsc);
    });
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
        syncColumnToggle();
    });
    $('column-box').addEventListener('toggle', () => {
        if (!$('column-box').open) $('column-box').classList.remove('active');
        syncColumnToggle();
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
        renderTemplateOptions();
        scheduleExportPreview();
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
    if (exportScope() === 'custom') return customResults;
    return table.getResults();
}

function exportColumns() {
    const scope = exportScope();
    if (scope === 'direct') return [...CSV_COLUMNS];
    if (scope === 'custom') return CSV_COLUMNS.filter(column => customColumnKeys.includes(column.key));
    return CSV_COLUMNS.filter(column => visibleColumnKeys.includes(column.key));
}

function currentExportContent() {
    const results = exportResults();
    if (!results.length) return '';
    if (exportFormat() === 'csv') {
        const columns = exportColumns();
        if (!columns.length) return '';
        return serializeExport(results, 'csv', { columns, enableTLS: $('lat-tls').checked });
    }
    return serializeExport(results, 'txt', { template: $('format-template').value });
}

function updateCustomExportUI() {
    const custom = exportScope() === 'custom';
    $('custom-export-actions').hidden = !custom;
    $('custom-field-picker').hidden = !custom || exportFormat() !== 'csv';
    $('custom-count').textContent = custom ? `当前 ${customResults.length} 条` : '';
    $('btn-custom-clear').disabled = !custom || customResults.length === 0;
}

function regenerateOutput() {
    clearTimeout(exportPreviewTimer);
    exportPreviewTimer = null;
    const results = exportResults();
    const content = currentExportContent();
    $('output-box').value = content;
    renderCSVPreview(results);
    $('btn-copy').disabled = !content;
    $('btn-download').disabled = !content;
    $('btn-import-lib').disabled = results.length === 0;
    $('output-count').textContent = `${results.length} 条`;
    $('output-title').textContent = `${exportFormat() === 'csv' ? 'CSV' : 'TXT'} 预览`;
    updateCustomExportUI();
}

function renderCSVPreview(results) {
    const csv = exportFormat() === 'csv';
    $('output-box').hidden = csv;
    $('csv-preview').hidden = !csv;
    if (!csv) return;
    const columns = exportColumns();
    $('csv-preview-head').innerHTML = columns.length
        ? `<tr>${columns.map(column => `<th>${escapeHTML(column.label)}</th>`).join('')}</tr>`
        : '';
    $('csv-preview-body').innerHTML = results.slice(0, 100).map(result =>
        `<tr>${columns.map(column => `<td>${escapeHTML(csvValue(column, result, { enableTLS: $('lat-tls').checked }))}</td>`).join('')}</tr>`
    ).join('');
    $('csv-preview-empty').hidden = Boolean(results.length && columns.length);
}

function renderCustomFieldOptions() {
    $('custom-field-count').textContent = `${customColumnKeys.length}/${CSV_COLUMNS.length}`;
    $('custom-field-options').innerHTML = CSV_COLUMNS.map(column =>
        `<label class="checkbox"><input type="checkbox" data-key="${column.key}" ${customColumnKeys.includes(column.key) ? 'checked' : ''}> ${escapeHTML(column.label)}</label>`
    ).join('');
}

function applyCustomColumnsFromUI() {
    const keys = [...document.querySelectorAll('#custom-field-options input:checked')].map(input => input.dataset.key);
    if (!keys.length) { toast('至少保留一个自定义字段'); renderCustomFieldOptions(); return; }
    customColumnKeys = keys;
    renderCustomFieldOptions();
    renderTemplateOptions();
    regenerateOutput();
}

function scheduleExportPreview() {
    clearTimeout(exportPreviewTimer);
    exportPreviewTimer = setTimeout(regenerateOutput, 140);
}

function appendSelectedToCustom() {
    if (!table) return;
    const selected = table.getSelectedResultsInDisplayOrder();
    if (!selected.length) { toast('请先勾选要追加的结果'); return; }
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
        const columns = exportColumns();
        const scope = exportScope();
        const scopeLabel = { direct: '全部字段', rules: '当前展示字段', custom: '自定义字段' }[scope] || '全部字段';
        $('format-presets').innerHTML = `<option value="csv:${scope}">${scopeLabel}（${columns.length} 列）</option>`;
        $('format-presets').value = `csv:${scope}`;
        txtEditor.hidden = true;
        csvHint.hidden = false;
        $('csv-column-label').textContent = columns.map(c => c.label).join('、') || '未选择字段';
        $('btn-delete-template').disabled = true;
        $('custom-field-picker').hidden = scope !== 'custom';
        return;
    }
    txtEditor.hidden = !templateEditorOpen;
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
            renderTemplateOptions();
            regenerateOutput();
        }));
    loadSavedTemplates();
    renderCustomFieldOptions();
    $('btn-custom-append').addEventListener('click', appendSelectedToCustom);
    $('btn-custom-clear').addEventListener('click', clearCustomResults);
    $('custom-field-options').addEventListener('change', applyCustomColumnsFromUI);
    $('btn-custom-fields-all').addEventListener('click', () => {
        customColumnKeys = CSV_COLUMNS.map(column => column.key);
        renderCustomFieldOptions();
        renderTemplateOptions();
        regenerateOutput();
    });
    $('btn-custom-fields-default').addEventListener('click', () => {
        customColumnKeys = CSV_COLUMNS.map(column => column.key);
        renderCustomFieldOptions();
        renderTemplateOptions();
        regenerateOutput();
    });
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
        try {
            await copyToClipboard(text);
            toast(`已复制 ${exportResults().length} 条${exportFormat() === 'csv' ? ' CSV' : ' TXT'}`);
        } catch (error) {
            toast(error.message);
        }
    });
    $('btn-import-lib').addEventListener('click', importResultsToLib);
    $('btn-download').addEventListener('click', () => {
        regenerateOutput();
        const text = $('output-box').value;
        if (!text) { toast('没有可下载的结果'); return; }
        const results = exportResults();
        if (exportFormat() === 'csv') {
            const columns = exportColumns();
            download('\uFEFF' + text, 'iptest-result.csv', 'text/csv;charset=utf-8');
            toast(`已下载 ${results.length} 条 × ${columns.length} 列 CSV`);
        } else {
            download(text, 'iptest-result.txt', 'text/plain;charset=utf-8');
            toast(`已下载 ${results.length} 条 TXT`);
        }
    });
    regenerateOutput();
}

function bindPageNav() {
    // 折叠状态持久化
    const SIDEBAR_KEY = 'iptest.sidebarCollapsed.v1';
    const sidebar = $('sidebar');
    try {
        if (localStorage.getItem(SIDEBAR_KEY) === '1') sidebar.classList.add('collapsed');
    } catch { /* 忽略 */ }
    // 帮助面板
    $('btn-help').addEventListener('click', () => { $('help-overlay').hidden = false; });
    $('btn-help-close').addEventListener('click', () => { $('help-overlay').hidden = true; });
    $('help-overlay').addEventListener('click', e => { if (e.target === $('help-overlay')) $('help-overlay').hidden = true; });

    $('sidebar-toggle').addEventListener('click', () => {
        const collapsed = sidebar.classList.toggle('collapsed');
        try { localStorage.setItem(SIDEBAR_KEY, collapsed ? '1' : '0'); } catch { /* 忽略 */ }
    });
    document.querySelectorAll('.side-item').forEach(item => {
        item.addEventListener('click', e => {
            e.preventDefault();
            const page = item.dataset.page;
            document.querySelectorAll('.side-item').forEach(i => i.classList.toggle('active', i === item));
            document.querySelectorAll('.page').forEach(p => p.classList.toggle('active', p.id === 'page-' + page));
        });
    });
}

// 第 1 步：从本地库导入候选
async function importFromLibrary() {
    const sel = $('import-library-select');
    const id = sel.value;
    if (!id) { toast('请先选择本地库'); return; }
    try {
        const libData = await api.fetchAutoLibrary({ lib: id, limit: 2000 });
        const targets = (libData.entries || [])
            .filter(e => e.ip && (e.status === 'active' || e.status === 'new'))
            .map(e => ({ ip: e.ip, port: Number(e.port) || 0 }));
        const { added } = addUnique(proxyCandidates, targets);
        renderCandidates();
        toast(`已从库导入 ${added} 条候选（库内共 ${libData.total} 条）`);
    } catch (error) {
        toast(`导入候选失败：${error.message}`);
    }
}

async function bindLibraryCandidateImport() {
    try {
        await refreshLibraryCandidateOptions();
    } catch { /* 忽略 */ }
}

// 侧边栏日志：所有日志集中在这里（运行进度 / 错误 / 调试记录）
function sideLog(line, kind = '') {
    const body = $('log-viewer');
    const div = document.createElement('div');
    div.className = `log-line ${kind}`;
    div.textContent = `[${new Date().toLocaleTimeString('zh-CN', { hour12: false })}] ${line}`;
    body.appendChild(div);
    while (body.children.length > 500) body.removeChild(body.firstChild);
    body.scrollTop = body.scrollHeight;
}

function bindDebugLog() {
    const checkbox = $('log-enable');
    checkbox.addEventListener('change', async () => {
        try {
            const config = await api.fetchConfig();
            await api.saveSettings({ ...(config.settings || {}), debugLog: checkbox.checked });
            toast(checkbox.checked ? '调试日志已开启（写入 data/logs/app.log）' : '调试日志已关闭');
        } catch (error) {
            toast(`保存失败：${error.message}`);
        }
    });
    $('log-refresh').addEventListener('click', async () => {
        try {
            const data = await api.fetchLog(300);
            const body = $('log-viewer');
            body.innerHTML = (data.lines || []).map(l => `<div class="log-line file">${escapeHTML(l)}</div>`).join('');
            body.scrollTop = body.scrollHeight;
        } catch (error) {
            toast(`读取日志失败：${error.message}`);
        }
    });
    $('log-clear').addEventListener('click', async () => {
        if (!confirm('确认清空日志文件？')) return;
        try {
            await api.clearLog();
            $('log-viewer').innerHTML = '';
            toast('日志已清空');
        } catch (error) {
            toast(`清空失败：${error.message}`);
        }
    });
}

// 导出 / 导入 本地库 弹窗 + 模板"＋"
function bindExportPopovers() {
    const open = id => { $(id).hidden = false; };
    const close = id => { $(id).hidden = true; };
    $('btn-export-open').addEventListener('click', () => open('export-modal'));
    $('btn-export-close').addEventListener('click', () => close('export-modal'));
    $('export-modal').addEventListener('click', e => { if (e.target === $('export-modal')) close('export-modal'); });

    $('btn-template-toggle').addEventListener('click', () => {
        templateEditorOpen = !templateEditorOpen;
        $('txt-template-editor').hidden = !templateEditorOpen;
    });
}

// 库变更后各入口实时刷新
function bindLibraryChanged() {
    window.addEventListener('library-changed', () => {
        refreshLibraryTargets();
        refreshLibraryCandidateOptions();
    });
}

async function refreshLibraryCandidateOptions() {
    try {
        const data = await api.fetchLibraries();
        const sel = $('import-library-select');
        sel.innerHTML = '<option value="">选择库…</option>'
            + (data.libraries || []).map(l => `<option value="${escapeHTML(l.id)}">${escapeHTML(l.name)}</option>`).join('');
    } catch { /* 忽略 */ }
}

async function init() {
    tasksPage = initTasks({ toast });
    libPage = initLibrary({ toast });
    bindPageNav();
    bindDebugLog();
    bindExportPopovers();
    bindLibraryChanged();
    bindLibraryCandidateImport();
    refreshLibraryTargets();
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
