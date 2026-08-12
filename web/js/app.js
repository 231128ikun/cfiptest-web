// app.js —— 主流程：候选准备 → 规则执行 → 结果整理 → 格式导出

import { getInputStats, smartFilter, parseFilterExpression, importCSVText } from './input.js';
import * as api from './api.js';
import { store, setMode, parseLines, targetToLine, entryKey } from './store.js';
import { ResultTable, CSV_COLUMNS } from './table.js';
import { ALL_COLUMNS, TABLE_COLUMNS, DEFAULT_BADGE_THRESHOLDS, csvValue, escapeHTML, normalizeBadgeThresholds, setBadgeThresholds } from './columns.js';
import { PRESETS, placeholderNames } from './composer.js';
import { download, copyToClipboard, serialize as serializeExport } from './exporter.js';
import { boundedNumber, parseRangeInput } from './validation.js';
import { initTasks } from './tasks.js';
import { initLibrary } from './library.js';
import { addQuotaRule as addQuotaRuleShared, readQuotaRules as readQuotaRulesShared, clearQuotaEditors as clearQuotaEditorsShared, refreshQuotaEditors as refreshQuotaEditorsShared } from './quota-rules.js';
import { SAVED_TEMPLATE_KEY, loadSavedTemplates as loadTemplatesFromStorage, templateOptionsHTML } from './templates.js';
import { refreshCloudConfigs, cloudConfigs, cloudChannels, channelLabel, fillCloudSelect } from './cloud.js';

const $ = id => document.getElementById(id);

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
// quota rules are now in shared module quota-rules.js

let officialEstimateTimer = null;
let exportPreviewTimer = null;
let savedTemplates = [];
let customResults = []; // 自定义导出列表，默认空，仅手动加入或勾选追加
let customColumnKeys = CSV_COLUMNS.map(column => column.key); // 自定义 CSV 字段，默认全部
// 候选漏斗：本批快照键、剩余数量与所属模式（模式说明见 startPipeline）
let batchKeys = null;          // Set<string>：本批开始时快照的候选键，续跑/暂停后清空
let batchRemaining = 0;        // 本批剩余未检测数量（含进行中）
let batchTotal = 0;            // 本批总数量（服务端解析后）
let batchMode = null;          // 'proxy' | 'official' | 'speed' | null
let renderCandidatesScheduled = false; // rAF 节流：高频 target_done 时避免反复拼接大数组
let officialRangesLoading = false;
const LOG_LEVEL_RANK = { debug: 0, info: 1, warn: 2, error: 3 };
let lastLogRawLines = []; // store raw log lines for client-side level filtering

const DEFAULT_COLUMN_KEYS = TABLE_COLUMNS.filter(c => c.key !== '_sel').map(c => c.key);
const SELECTABLE_COLUMN_KEYS = ALL_COLUMNS
    .filter(column => column.key !== '_sel' && column.key !== 'enableTLS' && column.inCSV)
    .map(column => column.key);
let visibleColumnKeys = [...DEFAULT_COLUMN_KEYS];
let templateEditorOpen = false; // 导出弹窗里“＋”新增 TXT 模板面板的展开状态

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
    const seen = new Set(base.map(entryKey));
    let added = 0;
    let duplicates = 0;
    for (const target of incoming) {
        if (!target?.ip) continue;
        const key = entryKey(target);
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

// 从指定候选数组按原始键移除一条（target_done 事件使用）
function removeCandidateByKey(list, key) {
    const idx = list.findIndex(t => entryKey(t) === key);
    if (idx === -1) return false;
    list.splice(idx, 1);
    return true;
}

// 高频 target_done 时只改数组，渲染合并到下一帧，避免每事件都 join 几千行
function renderCandidatesThrottled() {
    if (renderCandidatesScheduled) return;
    renderCandidatesScheduled = true;
    requestAnimationFrame(() => {
        renderCandidatesScheduled = false;
        renderCandidates();
    });
}

function updateRunButton() {
    const running = currentTaskId !== null;
    const active = activeCandidates();
    const targetReached = !running && ruleMaxResults() > 0 && remainingResultLimit() === 0;
    const btn = $('btn-start-latency');
    btn.disabled = !running && (active.length === 0 || targetReached);
    if (running) {
        btn.textContent = '停止检测';
    } else if (targetReached) {
        btn.textContent = '已达到目标';
    } else if (batchRemaining > 0 && batchKeys) {
        btn.textContent = '继续检测';
    } else {
        btn.textContent = '开始检测';
    }
}

function updateBatchStatus() {
    const el = $('batch-status');
    if (!el) return;
    const active = batchRemaining > 0 && batchKeys && activeCandidates().length > 0;
    el.hidden = !active;
    if (active) {
        $('batch-progress').textContent = `${batchTotal - batchRemaining}/${batchTotal}`;
    }
}

function renderCandidates() {
    $('ip-candidates').value = proxyCandidates.map(targetToLine).join('\n');
    $('official-candidates').value = officialCandidates.map(targetToLine).join('\n');
    $('official-candidate-count').textContent = `${officialCandidates.length} 条`;

    const active = activeCandidates();
    $('candidate-count').textContent = `${active.length} 条`;
    $('run-target-count').textContent = active.length;
    updateRunButton();
    updateBatchStatus();

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
        if (currentTaskId) { toast('请先暂停检测'); return; }
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

    function updateProxySourceUI({ focus = false } = {}) {
        const source = $('import-source').value;
        const urlInput = $('import-remote-url');
        const librarySelect = $('import-library-select');
        const hint = $('import-hint');
        const action = $('btn-import-remote');

        urlInput.hidden = source !== 'remote';
        librarySelect.hidden = source !== 'library';
        hint.hidden = source !== 'file';
        action.textContent = source === 'file' ? '选择文件' : source === 'remote' ? '载入链接' : '导入候选';
        action.disabled = source === 'library' && !librarySelect.value;

        if (!focus) return;
        if (source === 'remote') urlInput.focus();
        if (source === 'library') librarySelect.focus();
    }
    updateProxySourceUI();
    $('import-source').addEventListener('change', () => updateProxySourceUI({ focus: true }));
    $('import-remote-url').addEventListener('keydown', event => {
        if (event.key === 'Enter') $('btn-import-remote').click();
    });
    $('import-library-select').addEventListener('change', () => {
        $('btn-import-remote').disabled = !$('import-library-select').value;
    });
    $('btn-import-remote').addEventListener('click', async () => {
        const source = $('import-source').value;
        if (source === 'file') { $('file-input').click(); return; }
        if (source === 'library') { await importFromLibrary(); return; }
        const url = $('import-remote-url').value.trim();
        if (!url) { toast('请填写远程 TXT / CSV 地址'); $('import-remote-url').focus(); return; }
        const button = $('btn-import-remote');
        const idleText = '载入链接';
        button.disabled = true;
        button.textContent = '载入中…';
        try {
            const resp = await api.importRemote(url, { sampleMode: 'one', sampleN: 1 });
            const imported = resp.format === 'csv' ? importCSVText(resp.text || '') : (resp.text || '');
            appendRawText(imported || resp.targets.map(targetToLine).join('\n'));
            toast(`远程 ${resp.format === 'csv' ? 'CSV' : 'TXT'} 已载入原始列表`);
        } catch (error) {
            toast(error.message);
        } finally {
            button.disabled = false;
            button.textContent = idleText;
        }
    });
}

function officialSettings() {
    return {
        family: document.querySelector('input[name="official-family"]:checked')?.value || 'ipv4',
        sampleMode: document.querySelector('input[name="sample-mode"]:checked')?.value || 'one',
        sampleN: parseInt($('sample-n').value, 10) || 1,
        protocol: $('official-protocol').value === 'http' ? 'http' : 'https',
        port: parseInt($('official-port').value, 10) || ($('official-protocol').value === 'http' ? 80 : 443),
    };
}

function updateOfficialPortOptions(preferredPort) {
    const protocol = $('official-protocol').value === 'http' ? 'http' : 'https';
    const defaults = protocol === 'http' ? [80, 8080, 8880, 2052, 2082, 2086, 2095] : [443, 2053, 2083, 2087, 2096, 8443];
    const ports = officialRanges?.ports?.[protocol] || defaults;
    const selected = Number(preferredPort || $('official-port').value);
    $('official-port').innerHTML = ports.map(port => `<option value="${port}">${port}</option>`).join('');
    $('official-port').value = ports.includes(selected) ? String(selected) : String(ports[0]);
    $('official-port-summary').textContent = `${protocol.toUpperCase()} · ${$('official-port').value}`;
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
        updateOfficialPortOptions();
        renderRangesEstimate();
    } catch (error) {
        $('ranges-status').textContent = '加载失败';
        toast(error.message);
    } finally {
        officialRangesLoading = false;
    }
}

function renderRangesEstimate() {
    const { family, sampleMode, protocol, port } = officialSettings();
    const hint = $('sample-hint');
    hint.textContent = family === 'ipv4'
        ? `IPv4 按每个 /24 抽样，避免直接检测百万级地址；当前使用 ${protocol.toUpperCase()} 端口 ${port}。`
        : `IPv6 无法穷举全部地址（每个 /48 子网有 2^80 个地址），按每个 /48 子网抽样，每子网最多 256 个、最多覆盖 1024 个子网；当前使用 ${protocol.toUpperCase()} 端口 ${port}。`;
    $('official-port-summary').textContent = `${protocol.toUpperCase()} · ${port}`;
    if (!officialRanges) return;
    let count;
    if (family === 'ipv4') {
        if (sampleMode === 'one') count = officialRanges.estimate?.onePerSubnet;
        else if (sampleMode === 'n') count = officialRanges.estimate?.nPerSubnet;
        else count = officialRanges.estimate?.all;
    } else {
        if (sampleMode === 'one') count = officialRanges.estimate?.ipv6OnePerSubnet;
        else if (sampleMode === 'n') count = officialRanges.estimate?.ipv6NPerSubnet;
        else count = officialRanges.estimate?.ipv6All;
    }
    const warning = sampleMode === 'all' && family === 'ipv4' ? '，数量过大，不建议直接执行' : '';
    $('ranges-estimate').textContent = `预计生成 ${Number(count || 0).toLocaleString()} 个候选${warning}`;
}

async function generateOfficialCandidates() {
    if (currentTaskId) { toast('请先暂停检测'); return; }
    if (!officialRanges) { await fetchRanges(); }
    if (!officialRanges) return;
    const settings = officialSettings();
    if (settings.family === 'ipv4' && settings.sampleMode === 'all') {
        toast('IPv4 全部地址超过单次展开上限，请选择抽样');
        return;
    }
    const ranges = settings.family === 'ipv6' ? officialRanges.ipv6 : officialRanges.ipv4;
    const text = ranges.map(cidr => cidr.includes(':') ? `[${cidr}]:${settings.port}` : `${cidr}:${settings.port}`).join('\n');
    const button = $('btn-add-ranges');
    button.disabled = true;
    try {
        const resp = await api.importText(text, settings);
        officialCandidates = [];
        const { added } = addUnique(officialCandidates, resp.targets);
        $('lat-tls').checked = settings.protocol === 'https';
        updateDefaultPortHint();
        renderCandidates();
        toast(`已生成 ${added} 条官方候选（${settings.protocol.toUpperCase()}:${settings.port}）`);
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
        if (currentTaskId) { toast('请先暂停检测'); return; }
        officialCandidates = [];
        renderCandidates();
    });
    document.querySelectorAll('input[name="official-family"], input[name="sample-mode"]').forEach(input =>
        input.addEventListener('change', renderRangesEstimate));
    $('official-protocol').addEventListener('change', () => { updateOfficialPortOptions(); renderRangesEstimate(); });
    $('official-port').addEventListener('change', renderRangesEstimate);
    $('sample-n').addEventListener('input', () => {
        renderRangesEstimate();
        clearTimeout(officialEstimateTimer);
        officialEstimateTimer = setTimeout(async () => {
            if (!officialRanges) return;
            try {
                officialRanges = await api.fetchOfficialRanges(officialSettings().sampleN);
                updateOfficialPortOptions();
                renderRangesEstimate();
            } catch (error) {
                toast(error.message);
            }
        }, 250);
    });
    updateOfficialPortOptions();
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
        updateDefaultPortHint();
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

function qualifiedResultCount() {
    if (!table) return 0;
    const maxLatency = Number($('lat-maxlatency').value) || 0;
    const minSpeed = Number($('spd-minspeed').value) || 0;
    return table.results.filter(result => {
        if (maxLatency > 0 && Number(result.tcpLatencyMs) > maxLatency) return false;
        if (!speedEnabled()) return true;
        const speed = Number(result.downloadSpeedKBs) || 0;
        return speed > 0 && (minSpeed <= 0 || speed >= minSpeed);
    }).length;
}

// “满足条件后自动停止”约束累计的整体结果；停止后继续时只检测尚缺的数量。
function remainingResultLimit() {
    const target = ruleMaxResults();
    return target > 0 ? Math.max(0, target - qualifiedResultCount()) : 0;
}

function readNumberField(id, { min = -Infinity, max = Infinity, integer = false, optional = false } = {}) {
    const field = $(id);
    const result = boundedNumber(field.value, { min, max, integer, emptyValue: 0 });
    if (!result.empty) field.value = result.value;
    return result.empty && optional ? undefined : result.value;
}

function normalizeRuleFields({ notify = false } = {}) {
    const rules = [
        ['lat-concurrency', { min: 1, max: 1000, integer: true }],
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

function latencyOptions(maxResults = null) {
    normalizeRuleFields();
    return {
        maxConcurrency: readNumberField('lat-concurrency', { min: 1, max: 1000, integer: true, optional: true }),
        maxLatencyMs: readNumberField('lat-maxlatency', { min: 0, max: 10000, integer: true, optional: true }) || 0,
        // 启用速度规则时，统一数量限制只在最终测速阶段生效。
        maxResults: speedEnabled() ? 0 : (maxResults ?? ruleMaxResults()),
        enableTLS: $('lat-tls').checked,
        enableIPAPI: $('lat-ipapi').checked,
    };
}

function speedOptions(maxResults = null) {
    normalizeRuleFields();
    return {
        maxConcurrency: readNumberField('spd-concurrency', { min: 1, max: 100, integer: true, optional: true }),
        durationSec: readNumberField('spd-duration', { min: 1, max: 30, integer: true, optional: true }),
        minSpeedKBs: readNumberField('spd-minspeed', { min: 0, optional: true }) || 0,
        maxResults: maxResults ?? ruleMaxResults(),
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
    const hint = $('default-port-hint');
    if (store.mode === 'official') {
        const { protocol, port } = officialSettings();
        hint.textContent = `官方候选已写入 ${protocol.toUpperCase()} 端口 ${port}`;
        return;
    }
    const port = $('lat-tls').checked ? 443 : 80;
    hint.textContent = `目标自带端口优先；未指定时使用 ${port}`;
}

function resetRules() {
    const lat = defaults?.latency || { maxConcurrency: 100, timeoutMs: 3000, maxLatencyMs: 0, enableTLS: true, enableIPAPI: false };
    const spd = defaults?.speed || { maxConcurrency: 5, durationSec: 5, minSpeedKBs: 0, downloadURL: 'speed.cloudflare.com/__down?bytes=500000000' };
    $('lat-concurrency').value = lat.maxConcurrency;
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
    updateRunButton();
    updateBatchStatus();
    if (table) $('btn-clear-results').disabled = running || table.results.length === 0;
    $('btn-start-speed').disabled = running || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = running || table.getAllResults().length === 0;
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
    const target = ruleMaxResults();
    const remainingLimit = remainingResultLimit();
    if (target > 0 && remainingLimit === 0) {
        toast(`整体结果已达到 ${target} 个；如需继续，请提高数量目标或清空结果`);
        updateRunButton();
        return;
    }
    // 候选漏斗：只在本次开始时快照；结果不清空，停止后从剩余候选继续。
    const keys = new Set(targets.map(entryKey));
    $('progress-fill').style.width = '0%';
    try {
        const response = await api.startLatencyTest(targets, latencyOptions(remainingLimit), {
            enableSpeed: speedEnabled(),
            speedOptions: speedOptions(remainingLimit),
        });
        currentTaskId = response.taskId;
        batchKeys = keys;
        batchMode = store.mode;
        batchTotal = response.totalTargets;
        batchRemaining = keys.size;
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

function openImportTargetModal() {
    const results = exportResults();
    if (!results.length) { toast('当前导出范围没有结果'); return; }
    $('import-target-count').textContent = String(results.length);
    $('import-target-modal').hidden = false;
}

function closeImportTargetModal() {
    $('import-target-modal').hidden = true;
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
    $('spd-enable').addEventListener('change', () => {
        applySpeedEnabled();
        scheduleSettingsAutoSave();
    });
    ['badge-latency-green-end', 'badge-latency-yellow-end', 'badge-speed-red-end', 'badge-speed-yellow-end'].forEach(id => {
        $(id).addEventListener('input', previewBadgeThresholdsFromUI);
        $(id).addEventListener('change', () => {
            applyBadgeThresholdsFromUI();
            scheduleSettingsAutoSave();
        });
    });
    ['lat-concurrency', 'lat-maxlatency', 'spd-concurrency', 'spd-duration', 'spd-minspeed', 'rule-maxresults']
        .forEach(id => {
            // input 先防抖保存（防止输入后直接刷新丢失），change 时再归一化并重排保存
            $(id).addEventListener('input', scheduleSettingsAutoSave);
            $(id).addEventListener('change', () => {
                normalizeRuleFields({ notify: true });
                scheduleSettingsAutoSave();
            });
        });
    $('lat-tls').addEventListener('change', () => {
        $('spd-tls').checked = $('lat-tls').checked;
        updateDefaultPortHint();
        scheduleSettingsAutoSave();
    });
    $('lat-ipapi').addEventListener('change', scheduleSettingsAutoSave);
    $('advanced-speed-url').addEventListener('input', () => {
        $('spd-url').value = $('advanced-speed-url').value;
        scheduleConfigAutoSave();
    });
    $('rule-maxresults').addEventListener('input', () => { $('spd-maxresults').value = ruleMaxResults(); updateRunButton(); });
    document.getElementById('btn-reset-rules').addEventListener('click', () => { resetRules(); scheduleSettingsAutoSave(); toast('已恢复推荐设置'); });
    $('btn-save-settings').addEventListener('click', saveLocalSettings);
    $('btn-start-latency').addEventListener('click', async () => {
        if (currentTaskId !== null) {
            const btn = $('btn-start-latency');
            btn.disabled = true;
            btn.textContent = '正在停止…';
            try {
                await api.stopTask(currentTaskId);
                toast('已发送停止指令');
            } catch (error) {
                toast(error.message);
                updateRunButton();
            }
            return;
        }
        await startPipeline();
    });
    $('btn-start-speed').addEventListener('click', () => startSupplementalSpeed(false));
    $('btn-speed-filtered').addEventListener('click', () => startSupplementalSpeed(true));

}

function bindSettingsAutoSave() {
    // 设置页：自动维护参数改动后自动落盘
    ['auto-lat-concurrency', 'auto-lat-timeout', 'auto-lat-probes', 'auto-lat-http-probes',
     'auto-lat-http-timeout', 'auto-lat-remove-after', 'auto-spd-concurrency', 'auto-spd-duration']
        .forEach(id => $(id).addEventListener('change', scheduleSettingsAutoSave));
    // 设置页：数据源 / URL 类字段改动后自动保存到 config
    ['advanced-trace-url', 'advanced-ips-url', 'advanced-speed-url']
        .forEach(id => $(id).addEventListener('input', scheduleConfigAutoSave));
    // 多地址数据源：逐行编辑，增删行也会自动保存
    document.querySelectorAll('.list-editor').forEach(container => {
        bindListEditor(container, scheduleConfigAutoSave);
    });
    // 单地址行的清空按钮：清空后保存即回退默认
    document.querySelectorAll('[data-clear]').forEach(btn => {
        btn.addEventListener('click', () => {
            const input = $(btn.dataset.clear);
            if (!input) return;
            input.value = '';
            if (input.id === 'advanced-speed-url') $('spd-url').value = '';
            scheduleConfigAutoSave();
            toast('已清空，保存后将恢复默认');
        });
    });
    $('btn-reset-settings').addEventListener('click', resetSettingsToDefaults);
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
        onTargetDone: ev => { const target = ev?.target || ev;
            const key = target?.key || entryKey(target) || `${target.ip}|${target.port || 0}`;
            const list = batchMode === 'official' ? officialCandidates : proxyCandidates;
            const removed = removeCandidateByKey(list, key);
            if (removed && batchRemaining > 0) batchRemaining--;
            renderCandidatesThrottled();
        },
        onSpeed: result => {
            table.updateSpeed(result);
            scheduleExportPreview();
        },
        onAuto: message => { tasksPage?.onAuto(message); sideLogFromAuto(message); },
        onDone: (message, reason) => {
            if (tasksPage?.isAutoRunning()) { tasksPage.onDone(message, reason); return; }
            const isPipeline = currentTaskType === 'pipeline';
            setRunning(false);
            if (isPipeline && reason === 'completed') {
                // 本批全部测完，漏斗收尾；运行期间新补充的候选保留，可继续检测
                const leftover = activeCandidates().length;
                batchKeys = null;
                batchMode = null;
                batchRemaining = 0;
                $('progress-label').textContent = leftover > 0 ? `已完成，候选剩余 ${leftover} 条` : '已完成';
            } else if (isPipeline && reason === 'limit') {
                // 达到最大结果数：剩余候选未测完，保留漏斗，可点“继续检测”
                $('progress-label').textContent = batchRemaining > 0 ? `已达到最大数量，候选剩余 ${batchRemaining} 条` : '已达到最大数量';
            } else if (isPipeline && reason === 'stopped') {
                $('progress-label').textContent = batchRemaining > 0 ? `已暂停，候选剩余 ${batchRemaining} 条` : '已停止';
            } else {
                $('progress-label').textContent = reason === 'limit' ? '已达到最大数量' : reason === 'stopped' ? '已停止' : '已完成';
            }
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
    window.addEventListener('pagehide', () => flushAutoSaves({ unload: true }));
}

// SSE auto 事件转成侧边栏日志行
function sideLogFromAuto(message) {
    if (!message) return;
    let p;
    try { p = JSON.parse(message); } catch { return; }
    if (p.stage === 'cloud') {
        const task = p.task ? `任务「${p.task}」` : '维护任务';
        if (p.status === 'uploading') sideLog(`${task}开始上传至云端：${p.key || '默认路径'}`);
        else if (p.status === 'success') sideLog(`${task}云端同步成功：${p.url || p.key || '已完成'}`, 'ok');
        else if (p.status === 'error') sideLog(`${task}云端同步失败：${p.error || '未知错误'}`, 'error');
        return;
    }
    if (p.stage === 'report' && p.report) {
        const r = p.report;
        const shortage = (r.shortages || []).length ? `，缺口 ${r.shortages.length} 项` : '';
        sideLog(`任务「${r.subscription || ''}」完成：输出 ${r.totalLines ?? 0} 行，移除失效 ${r.removedDead ?? 0}，标记保留 ${r.markedFailed ?? 0}，回写 ${(r.groups || []).reduce((s, g) => s + (g.updated || 0), 0)}${shortage}`, 'ok');
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
    $('column-save-status').textContent = '字段有修改，将自动保存';
    scheduleSettingsAutoSave();
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
    const lat = parseRangeInput($('result-latency-range').value, { singleBias: 'max' });
    const spd = parseRangeInput($('result-speed-range').value, { singleBias: 'min' });
    table.setFilters({
        minLatency: lat.min || 0,
        maxLatency: lat.max || 0,
        minSpeed: spd.min || 0,
        maxSpeed: spd.max || 0,
    });
    refreshButtons();
    refreshQuotaEditors();
    scheduleExportPreview();
}



function addQuotaRule(seed = {}) { addQuotaRuleShared(document.getElementById('quota-rules'), table, seed); }

function readQuotaRules() { return readQuotaRulesShared(); }


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
        autoLatencyConcurrency: positiveInt('auto-lat-concurrency'),
        autoLatencyTimeoutMs: positiveInt('auto-lat-timeout'),
        autoLatencyProbes: positiveInt('auto-lat-probes'),
        autoLatencyHTTPProbes: positiveInt('auto-lat-http-probes'),
        autoLatencyHTTPTimeoutMs: positiveInt('auto-lat-http-timeout'),
        autoRemoveAfterFailures: positiveInt('auto-lat-remove-after'),
        autoSpeedConcurrency: positiveInt('auto-spd-concurrency'),
        autoSpeedDurationSec: positiveInt('auto-spd-duration'),
        // 日志开关/级别也随设置持久化，避免整表保存覆盖时把它们冲掉。
        debugLog: $('log-enable').checked,
        logLevel: $('log-level').value,
    };
}

function positiveInt(id) {
    const v = Number($(id).value);
    return Number.isFinite(v) && v > 0 ? v : 0;
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

/** 设置页自动维护参数的内置默认值（与后端 subscription 默认一致）。 */
const RESET_MAINTENANCE_DEFAULTS = {
    latencyConcurrency: 50,
    latencyTimeoutMs: 3000,
    latencyProbes: 4,
    latencyHTTPProbes: 1,
    latencyHTTPTimeoutMs: 3000,
    removeAfterFailures: 3,
    speedConcurrency: 5,
    speedDurationSec: 5,
};

function createListEditorRow(value = '') {
    const row = document.createElement('div');
    row.className = 'list-editor-row';
    const input = document.createElement('input');
    input.type = 'text';
    input.value = value;
    input.spellcheck = false;
    input.placeholder = '输入地址，如 https://…';
    input.setAttribute('aria-label', '数据源地址');
    const del = document.createElement('button');
    del.type = 'button';
    del.className = 'row-action list-del';
    del.textContent = '×';
    del.title = '删除该地址';
    del.setAttribute('aria-label', '删除该地址');
    row.append(input, del);
    return row;
}

function setListEditor(containerId, values) {
    const container = $(containerId);
    if (!container) return;
    const rows = container.querySelector('[data-role="rows"]');
    if (!rows) return;
    rows.textContent = '';
    const items = Array.isArray(values) && values.length ? values : [''];
    items.forEach(value => rows.append(createListEditorRow(value)));
}

function getListEditor(containerId) {
    const container = $(containerId);
    if (!container) return [];
    return [...container.querySelectorAll('[data-role="rows"] input')]
        .map(input => input.value.trim())
        .filter(Boolean);
}

function bindListEditor(container, onChange) {
    container.addEventListener('input', event => {
        if (event.target.matches('input[type="text"]')) onChange();
    });
    container.addEventListener('click', event => {
        const add = event.target.closest('[data-role="add"]');
        if (add) {
            const rows = container.querySelector('[data-role="rows"]');
            const row = createListEditorRow();
            rows.append(row);
            row.querySelector('input').focus();
            onChange();
            return;
        }
        const del = event.target.closest('.list-del');
        if (del) {
            const row = del.closest('.list-editor-row');
            row.remove();
            const rows = container.querySelector('[data-role="rows"]');
            if (!rows.querySelector('.list-editor-row')) rows.append(createListEditorRow());
            onChange();
        }
    });
}

function resetMaintenanceFieldsToDefaults() {
    const d = RESET_MAINTENANCE_DEFAULTS;
    $('auto-lat-concurrency').value = d.latencyConcurrency;
    $('auto-lat-timeout').value = d.latencyTimeoutMs;
    $('auto-lat-probes').value = d.latencyProbes;
    $('auto-lat-http-probes').value = d.latencyHTTPProbes;
    $('auto-lat-http-timeout').value = d.latencyHTTPTimeoutMs;
    $('auto-lat-remove-after').value = d.removeAfterFailures;
    $('auto-spd-concurrency').value = d.speedConcurrency;
    $('auto-spd-duration').value = d.speedDurationSec;
    fillBadgeThresholdFields({});
}

async function resetSettingsToDefaults() {
    if (!confirm('确定恢复全部默认设置吗？当前「设置」页内容将被覆盖并立即保存。')) return;
    try {
        const response = await api.resetConfig();
        fillConfigFields(response.config);
        resetMaintenanceFieldsToDefaults();
        $('spd-url').value = $('advanced-speed-url').value;
        await saveLocalSettings();
        $('settings-status').textContent = '已恢复为默认设置并保存';
        toast('已恢复全部默认设置');
    } catch (error) {
        toast(error.message);
    }
}

function fillConfigFields(config) {
    appConfig = config || {};
    const sources = appConfig.sources || {};
    $('advanced-speed-url').value = appConfig.speedTestURL || '';
    $('advanced-trace-url').value = appConfig.traceURL || '';
    $('advanced-ips-url').value = appConfig.ipsTypeURL || '';
    setListEditor('editor-locations', sources.locations);
    setListEditor('editor-asn', sources.asnDatabase);
    setListEditor('editor-official', sources.officialRanges);
    setListEditor('editor-official-v6', sources.activeIPv6RangeSources);
}

function applySavedSettings(settings = {}) {
    const rules = settings.rules || {};
    const lat = rules.latency || {};
    const spd = rules.speed || {};
    if (lat.maxConcurrency) $('lat-concurrency').value = lat.maxConcurrency;
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
    $('log-level').value = settings.logLevel === 'all' ? 'debug' : (settings.logLevel || 'debug');
    $('log-level').disabled = !$('log-enable').checked;
    debugLogStatus($('log-enable').checked);
    if (Array.isArray(settings.savedTemplates)) {
        savedTemplates = settings.savedTemplates
            .filter(item => item && typeof item.name === 'string' && typeof item.template === 'string');
    }
    if (Number(settings.autoLatencyConcurrency) > 0) $('auto-lat-concurrency').value = settings.autoLatencyConcurrency;
    if (Number(settings.autoLatencyTimeoutMs) > 0) $('auto-lat-timeout').value = settings.autoLatencyTimeoutMs;
    if (Number(settings.autoLatencyProbes) > 0) $('auto-lat-probes').value = settings.autoLatencyProbes;
    if (Number(settings.autoLatencyHTTPProbes) > 0) $('auto-lat-http-probes').value = settings.autoLatencyHTTPProbes;
    if (Number(settings.autoLatencyHTTPTimeoutMs) > 0) $('auto-lat-http-timeout').value = settings.autoLatencyHTTPTimeoutMs;
    if (Number(settings.autoRemoveAfterFailures) > 0) $('auto-lat-remove-after').value = settings.autoRemoveAfterFailures;
    if (Number(settings.autoSpeedConcurrency) > 0) $('auto-spd-concurrency').value = settings.autoSpeedConcurrency;
    if (Number(settings.autoSpeedDurationSec) > 0) $('auto-spd-duration').value = settings.autoSpeedDurationSec;
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

// ---- 设置 / 配置自动保存（防抖） ----
// 之前只有「保存到本地」按钮才写盘，中途改动一刷新就丢。现在规则、显示、
// 导出、维护参数等字段修改后 600ms 自动落盘（/api/settings）；数据源与 URL
// 类字段走 /api/config。页面卸载（刷新/关闭）前立即冲刷，避免丢失最后几步修改。
let settingsSavePending = false;
let configSavePending = false;
let autoSaveTimer = null;
const AUTO_SAVE_DELAY = 600;

function scheduleSettingsAutoSave() {
    settingsSavePending = true;
    if (!autoSaveTimer) autoSaveTimer = setTimeout(flushAutoSaves, AUTO_SAVE_DELAY);
}

function scheduleConfigAutoSave() {
    configSavePending = true;
    if (!autoSaveTimer) autoSaveTimer = setTimeout(flushAutoSaves, AUTO_SAVE_DELAY);
}

function readConfigFields() {
    return {
        ...(appConfig || {}),
        traceURL: $('advanced-trace-url').value.trim(),
        ipsTypeURL: $('advanced-ips-url').value.trim(),
        speedTestURL: $('advanced-speed-url').value.trim(),
        sources: {
            locations: getListEditor('editor-locations'),
            asnDatabase: getListEditor('editor-asn'),
            officialRanges: getListEditor('editor-official'),
            activeIPv6RangeSources: getListEditor('editor-official-v6'),
        },
    };
}

function flushAutoSaves({ unload = false } = {}) {
    if (autoSaveTimer) { clearTimeout(autoSaveTimer); autoSaveTimer = null; }
    const init = unload ? { keepalive: true } : {};
    if (settingsSavePending) {
        settingsSavePending = false;
        let settings = null;
        try { settings = currentSettings(); } catch { settings = null; }
        if (settings) {
            const request = api.saveSettings(settings, init);
            if (!unload) {
                request.then(() => {
                    $('settings-status').textContent = '已自动保存到 data 目录';
                    $('column-save-status').textContent = '显示字段已自动保存';
                }).catch(error => toast(`设置自动保存失败：${error.message}`));
            }
        }
    }
    if (configSavePending) {
        configSavePending = false;
        let cfg = null;
        try { cfg = readConfigFields(); } catch { cfg = null; }
        if (cfg) {
            const request = api.saveConfig(cfg, init);
            if (!unload) {
                request.then(response => { if (response?.config) appConfig = response.config; })
                    .catch(error => toast('配置自动保存失败：' + error.message));
            }
        }
    }
}

async function saveLocalSettings() {
    normalizeRuleFields({ notify: true });
    applyBadgeThresholdsFromUI();
    const cfg = readConfigFields();
    try {
        const response = await api.saveConfig(cfg);
        appConfig = response.config;
        await api.saveSettings(currentSettings());
        $('settings-status').textContent = '已保存；数据源、Trace 与 IPS 地址修改重启后完全生效';
        toast('设置已保存到 data 目录');
    } catch (error) { toast(error.message); }
}

function clearQuotaEditors(container) { clearQuotaEditorsShared(container); }

function refreshQuotaEditors() { refreshQuotaEditorsShared(table); }

function bindQuotaPanel() {
    $('btn-quota-add-rule').addEventListener('click', () => { addQuotaRule(); scheduleSettingsAutoSave(); });
    $('btn-quota-toggle').addEventListener('click', () => {
        const box = $('quota-box');
        const open = !box.classList.contains('active');
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
        scheduleSettingsAutoSave();
    });
    $('btn-quota-clear').addEventListener('click', () => {
        table.clearDisplayRules();
        clearQuotaEditors($('quota-rules'));
        addQuotaRule();
        refreshButtons();
        scheduleExportPreview();
        scheduleSettingsAutoSave();
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
    $('btn-clear-results').disabled = running || table.results.length === 0;
    $('btn-start-speed').disabled = running || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = running || table.getAllResults().length === 0;
    $('btn-custom-append').disabled = running || table.getSelectedResultsInDisplayOrder().length === 0;
}

function bindResults() {
    table = new ResultTable($('result-table-container'));
    renderColumnOptions();
    renderSortOptions();
    bindQuotaPanel();

    $('btn-clear-results').addEventListener('click', () => {
        if (currentTaskId) { toast('请先暂停检测'); return; }
        if (!table.results.length) return;
        if (!confirm('确定清空全部检测结果？')) return;
        table.clear();
        customResults = [];
        $('result-count').textContent = '';
        updateRunButton();
        refreshButtons();
        regenerateOutput();
        toast('已清空检测结果，可继续检测');
    });

    $('result-filter').addEventListener('input', applyResultFilters);
    $('result-latency-range').addEventListener('input', applyResultFilters);
    $('result-speed-range').addEventListener('input', applyResultFilters);
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
        $('column-save-status').textContent = '已恢复默认，将自动保存';
        scheduleSettingsAutoSave();
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
    const cloudBtn = $('btn-export-cloud');
    if (cloudBtn) cloudBtn.disabled = !content || !$('export-cloud-config').value;
    if (!content) { const cr = $('cloud-export-result'); if (cr) cr.hidden = true; }
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
    const seen = new Set(customResults.map(entryKey));
    let added = 0;
    let duplicates = 0;
    for (const result of selected) {
        const key = entryKey(result);
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
    const templateField = $('export-template-field');
    const templateToggle = $('btn-template-toggle');
    const txtEditor = $('txt-template-editor');
    const csvHint = $('csv-template-hint');
    if (format === 'csv') {
        const columns = exportColumns();
        const scope = exportScope();
        const scopeLabel = { direct: '全部字段', rules: '当前展示字段', custom: '自定义字段' }[scope] || '全部字段';
        $('format-presets').innerHTML = `<option value="csv:${scope}">${scopeLabel}（${columns.length} 列）</option>`;
        $('format-presets').value = `csv:${scope}`;
        templateEditorOpen = false;
        templateField.hidden = true;
        templateToggle.setAttribute('aria-expanded', 'false');
        txtEditor.hidden = true;
        csvHint.hidden = false;
        $('csv-column-label').textContent = columns.map(c => c.label).join('、') || '未选择字段';
        $('btn-delete-template').disabled = true;
        $('custom-field-picker').hidden = scope !== 'custom';
        return;
    }
    templateField.hidden = false;
    templateToggle.setAttribute('aria-expanded', String(templateEditorOpen));
    txtEditor.hidden = !templateEditorOpen;
    csvHint.hidden = true;
    $('format-presets').innerHTML = templateOptionsHTML(savedTemplates);
    $('format-presets').value = selected || 'preset:0';
    $('btn-delete-template').disabled = !$('format-presets').value.startsWith('saved:');
}

function loadSavedTemplates() {
    savedTemplates = loadTemplatesFromStorage();
    renderTemplateOptions();
}

function bindExport() {
    const templates = $('format-presets');
    $('export-format').addEventListener('change', () => {
        renderTemplateOptions();
        regenerateOutput();
        scheduleSettingsAutoSave();
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
        scheduleSettingsAutoSave();
    });
    $('format-template').addEventListener('input', () => {
        regenerateOutput();
        scheduleSettingsAutoSave();
    });
    const placeholders = placeholderNames();
    $('placeholder-count').textContent = `（${placeholders.length} 个）`;
    $('placeholder-help').innerHTML = placeholders.map(name => `<code data-ph="${name}">${name}</code>`).join('');
    $('placeholder-help').addEventListener('click', event => {
        const placeholder = event.target.dataset?.ph;
        if (!placeholder) return;
        $('format-template').value += placeholder;
        regenerateOutput();
        scheduleSettingsAutoSave();
    });
    document.querySelectorAll('input[name="export-scope"]').forEach(input =>
        input.addEventListener('change', () => {
            if (!input.checked) return;
            renderTemplateOptions();
            regenerateOutput();
            scheduleSettingsAutoSave();
        }));
    loadSavedTemplates();
    renderCustomFieldOptions();
    $('btn-custom-append').addEventListener('click', appendSelectedToCustom);
    $('btn-custom-clear').addEventListener('click', clearCustomResults);
    $('custom-field-options').addEventListener('change', () => {
        applyCustomColumnsFromUI();
        scheduleSettingsAutoSave();
    });
    $('btn-custom-fields-all').addEventListener('click', () => {
        customColumnKeys = CSV_COLUMNS.map(column => column.key);
        renderCustomFieldOptions();
        renderTemplateOptions();
        regenerateOutput();
        scheduleSettingsAutoSave();
    });
    $('btn-custom-fields-default').addEventListener('click', () => {
        customColumnKeys = CSV_COLUMNS.map(column => column.key);
        renderCustomFieldOptions();
        renderTemplateOptions();
        regenerateOutput();
        scheduleSettingsAutoSave();
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
    $('btn-import-lib').addEventListener('click', openImportTargetModal);
    $('btn-import-target-close').addEventListener('click', closeImportTargetModal);
    $('btn-import-target-cancel').addEventListener('click', closeImportTargetModal);
    $('import-target-modal').addEventListener('click', e => { if (e.target === $('import-target-modal')) closeImportTargetModal(); });
    $('btn-import-target-confirm').addEventListener('click', async () => {
        await importResultsToLib();
        closeImportTargetModal();
    });
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
    // ---- 导出至云端 ----
    $('export-cloud-config').addEventListener('change', () => {
        $('btn-export-cloud').disabled = !$('export-cloud-config').value || !$('output-box').value;
    });
    $('btn-export-cloud').addEventListener('click', handleCloudExport);
    $('btn-cloud-export-copy').addEventListener('click', async () => {
        const url = $('cloud-export-url').getAttribute('href');
        if (!url) return;
        try { await copyToClipboard(url); toast('云端链接已复制'); } catch (error) { toast(error.message); }
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
    $('btn-stop-power').addEventListener('click', () => {
        const confirmBtn = $('btn-exit-confirm');
        confirmBtn.disabled = false;
        confirmBtn.textContent = '确认退出';
        $('exit-overlay').hidden = false;
    });
    $('btn-exit-cancel').addEventListener('click', () => { $('exit-overlay').hidden = true; });
    $('exit-overlay').addEventListener('click', e => { if (e.target === $('exit-overlay')) $('exit-overlay').hidden = true; });
    $('btn-exit-confirm').addEventListener('click', async () => {
        $('btn-exit-confirm').disabled = true;
        $('btn-exit-confirm').textContent = '正在退出…';
        $('btn-exit-cancel').disabled = true;
        try {
            await fetch('/api/shutdown', { method: 'POST' });
        } catch { /* 服务已停止 */ }
        if (window.iptestAndroid?.closeApp) {
            window.iptestAndroid.closeApp();
            return;
        }
        // 用 exit-overlay 本身展示成功状态
        const dialog = document.querySelector('.exit-dialog');
        if (dialog) {
            dialog.innerHTML = '<p class="exit-dialog-title">服务已停止</p><p class="exit-dialog-text">页面即将关闭，请重新启动程序以继续使用。</p><div class="exit-dialog-actions"><button class="exit-confirm-btn" onclick="window.close()">关闭页面</button></div>';
        }
        setTimeout(() => { try { window.close(); } catch { /* 浏览器阻止，保留按钮 */ } }, 1500);
    });
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
function clearLogView() {
    const body = $('log-viewer');
    body.innerHTML = '';
    body.classList.add('is-disabled');
}

function debugLogStatus(enabled, message = '') {
    const status = $('log-status');
    if (!status) return;
    status.classList.toggle('is-enabled', enabled);
    status.classList.toggle('is-error', Boolean(message));
    const levelLabel = $('log-level').value || 'debug';
    status.textContent = message || (enabled
        ? `调试日志已开启：请求、任务与错误写入 data/logs/app.log（级别：${levelLabel}）。`
        : '调试日志已关闭：不记录不显示，磁盘上也不保留日志文件。');
}

function sideLog(line, kind = '') {
    const checkbox = $('log-enable');
    if (checkbox && !checkbox.checked) return;
    const body = $('log-viewer');
    if (!body) return;
    body.classList.remove('is-disabled');
    const div = document.createElement('div');
    div.className = `log-line ${kind}`;
    div.textContent = `[${new Date().toLocaleTimeString('zh-CN', { hour12: false })}] ${line}`;
    body.appendChild(div);
    while (body.children.length > 500) body.removeChild(body.firstChild);
    body.scrollTop = body.scrollHeight;
}

// level -> existing CSS color classes (error/warn/info=ok/debug=file)
const LOG_KIND = { error: 'error', warn: 'warn', info: 'ok', debug: 'file' };

function renderFilteredLogLines(lines, level) {
    const body = $('log-viewer');
    if (!body) return;
    const showAll = level === 'all' || !(level in LOG_LEVEL_RANK);
    const rank = LOG_LEVEL_RANK[level];
    body.innerHTML = lines
        .filter(l => {
            if (showAll) return true;
            const m = l.match(/^\S+\s+\[(\w+)\]/);
            if (!m) return true; // lines without level tag always show
            const lineRank = LOG_LEVEL_RANK[m[1].toLowerCase()];
            return lineRank != null ? lineRank >= rank : true;
        })
        .map(logLineHTML)
        .join('');
    body.scrollTop = body.scrollHeight;
}

function logLineHTML(l) {
    const m = l.match(/^\S+\s+\[(\w+)\]/);
    const kind = m ? (LOG_KIND[m[1].toLowerCase()] || 'file') : 'file';
    return `<div class="log-line ${kind}">${escapeHTML(l)}</div>`;
}

function bindDebugLog() {
    const checkbox = $('log-enable');
    const body = $('log-viewer');
    checkbox.addEventListener('change', async () => {
        const enabled = checkbox.checked;
        if (!enabled) clearLogView();
        debugLogStatus(enabled);
        $('log-level').disabled = !enabled;
        try {
            await api.saveSettingsPatch({ debugLog: enabled });
            toast(enabled ? '调试日志已开启（写入 data/logs/app.log）' : '调试日志已关闭');
        } catch (error) {
            checkbox.checked = !enabled;
            debugLogStatus(checkbox.checked, '保存日志设置失败：' + error.message);
            toast('保存失败：' + error.message);
        }
    });
    $('log-refresh').addEventListener('click', async () => {
        if (!checkbox.checked) {
            clearLogView();
            debugLogStatus(false);
            return;
        }
        try {
            const data = await api.fetchLog(300);
            body.classList.remove('is-disabled');
            lastLogRawLines = data.lines || [];
            renderFilteredLogLines(lastLogRawLines, $('log-level').value);
            debugLogStatus(data.enabled === true, data.writeError ? '日志写入异常：' + data.writeError : '');
        } catch (error) {
            debugLogStatus(true, '读取日志失败：' + error.message);
            toast('读取日志失败：' + error.message);
        }
    });
    $('log-clear').addEventListener('click', async () => {
        if (!confirm('确认清空日志文件？')) return;
        try {
            await api.clearLog();
            body.innerHTML = '';
            lastLogRawLines = [];
            body.classList.toggle('is-disabled', !checkbox.checked);
            toast('日志已清空');
        } catch (error) {
            toast('清空失败：' + error.message);
        }
    });
    $('log-level').addEventListener('change', async () => {
        renderFilteredLogLines(lastLogRawLines, $('log-level').value);
        try {
            await api.saveSettingsPatch({ logLevel: $('log-level').value });
            toast('日志级别已更新');
        } catch (error) {
            toast('保存失败：' + error.message);
        }
    });
    debugLogStatus(checkbox.checked);
}

// 导出 / 导入 本地库 弹窗 + 模板"＋"
function bindExportPopovers() {
    const open = id => { $(id).hidden = false; };
    const close = id => { $(id).hidden = true; };
    $('btn-export-open').addEventListener('click', () => open('export-modal'));
    $('btn-export-close').addEventListener('click', () => close('export-modal'));
    $('export-modal').addEventListener('click', e => { if (e.target === $('export-modal')) close('export-modal'); });
    document.addEventListener('keydown', e => {
        if (e.key !== 'Escape') return;
        if (!$('import-target-modal').hidden) { closeImportTargetModal(); return; }
        if (!$('export-modal').hidden) close('export-modal');
    });

    $('btn-template-toggle').addEventListener('click', () => {
        if (exportFormat() !== 'txt') return;
        templateEditorOpen = !templateEditorOpen;
        const toggle = $('btn-template-toggle');
        toggle.setAttribute('aria-expanded', String(templateEditorOpen));
        $('txt-template-editor').hidden = !templateEditorOpen;
        if (!templateEditorOpen) return;

        // “＋”始终进入新增状态；保留当前模板内容，便于基于现有模板创建变体。
        $('template-name').value = '';
        $('btn-delete-template').disabled = true;
        $('template-name').focus();
    });
}

// ---- 云端存储：设置页配置管理 + 导出至云端 ----
async function refreshCloudUI() {
    const note = $('cloud-export-note');
    try {
        await refreshCloudConfigs();
    } catch (error) {
        if (note) note.textContent = `云端配置加载失败：${error.message}`;
        return;
    }
    const configs = cloudConfigs();
    const channels = cloudChannels();
    fillCloudSelect($('export-cloud-config'));
    fillCloudSelect($('task-cloud-select'));
    const channelSel = $('cloud-config-channel');
    if (channelSel) {
        channelSel.innerHTML = channels.map(c => `<option value="${escapeHTML(c.id)}">${escapeHTML(c.name)}</option>`).join('') || '<option value="">（无可用渠道）</option>';
    }
    renderCloudConfigList();
    if (note) {
        note.textContent = configs.length
            ? `已配置 ${configs.length} 个云端配置；单文件 ≤ 1MB，路径建议使用字母/数字/下划线/点/斜杠。`
            : '尚未配置云端存储，请前往「设置 → 云端存储」添加配置。';
    }
    $('btn-export-cloud').disabled = !$('export-cloud-config').value || !$('output-box').value;
}

function renderCloudConfigList() {
    const wrap = $('cloud-config-list');
    const configs = cloudConfigs();
    if (!configs.length) {
        wrap.innerHTML = '<p class="cloud-config-empty">暂无云端配置。展开上方「＋ 添加 / 编辑配置」添加第一个（EdgeOne Blob）。</p>';
        return;
    }
    wrap.innerHTML = configs.map(c => `
        <div class="cloud-config-item">
            <div class="cloud-config-item-main">
                <div class="cloud-config-item-name">${escapeHTML(c.name)}</div>
                <div class="cloud-config-item-meta">${escapeHTML(channelLabel(c.channel))} · ${escapeHTML(c.baseUrl)} · Token ${escapeHTML(c.token || '')}</div>
            </div>
            <div class="cloud-config-item-actions">
                <button type="button" class="small" data-cloud-edit="${escapeHTML(c.id)}">编辑</button>
                <button type="button" class="small danger" data-cloud-del="${escapeHTML(c.id)}">删除</button>
            </div>
        </div>`).join('');
    wrap.querySelectorAll('[data-cloud-edit]').forEach(btn => btn.addEventListener('click', () => openCloudEditor(btn.dataset.cloudEdit)));
    wrap.querySelectorAll('[data-cloud-del]').forEach(btn => btn.addEventListener('click', () => deleteCloudConfig(btn.dataset.cloudDel)));
}

function cloudFormPayload() {
    return {
        name: $('cloud-config-name').value.trim(),
        channel: $('cloud-config-channel').value,
        baseUrl: $('cloud-config-base-url').value.trim(),
        token: $('cloud-config-token').value.trim(),
    };
}

function openCloudEditor(id = '') {
    const cfg = cloudConfigs().find(c => c.id === id) || {};
    $('cloud-config-id').value = cfg.id || '';
    $('cloud-config-name').value = cfg.name || '';
    if (cfg.channel) $('cloud-config-channel').value = cfg.channel;
    $('cloud-config-base-url').value = cfg.baseUrl || '';
    $('cloud-config-token').value = '';
    $('cloud-config-status').textContent = '';
    $('cloud-config-editor').open = true;
    $('cloud-config-name').focus();
}

function closeCloudEditor() {
    $('cloud-config-editor').open = false;
    $('cloud-config-id').value = '';
    $('cloud-config-name').value = '';
    $('cloud-config-channel').value = cloudChannels()[0]?.id || '';
    $('cloud-config-base-url').value = '';
    $('cloud-config-token').value = '';
    $('cloud-config-status').textContent = '';
}

async function saveCloudConfig() {
    const status = $('cloud-config-status');
    const id = $('cloud-config-id').value;
    const payload = cloudFormPayload();
    if (!payload.name) { status.textContent = '请填写配置名称'; return; }
    if (!payload.baseUrl) { status.textContent = '请填写站点地址'; return; }
    if (!id && !payload.token) { status.textContent = '新配置需要填写 Token'; return; }
    status.textContent = '保存中…';
    try {
        if (id) await api.updateCloudConfig(id, payload);
        else await api.createCloudConfig(payload);
        await refreshCloudUI();
        status.textContent = '已保存';
        closeCloudEditor();
        toast('云端配置已保存');
    } catch (error) {
        status.textContent = `保存失败：${error.message}`;
    }
}

async function testCloudConfig() {
    const status = $('cloud-config-status');
    const id = $('cloud-config-id').value;
    const payload = cloudFormPayload();
    if (!payload.baseUrl) { status.textContent = '请填写站点地址'; return; }
    if (!id && !payload.token) { status.textContent = '新配置需要填写 Token 才能测试'; return; }
    status.textContent = '测试中…';
    try {
        const data = await api.testCloud(id ? { id } : payload);
        status.textContent = data.ok ? '连接成功 ✓' : `连接失败：${data.error || '未知错误'}`;
        toast(data.ok ? '云端连接成功' : '云端连接失败');
    } catch (error) {
        status.textContent = `测试失败：${error.message}`;
    }
}

async function deleteCloudConfig(id) {
    const cfg = cloudConfigs().find(c => c.id === id);
    if (!cfg) return;
    if (!confirm(`确认删除云端配置「${cfg.name}」？`)) return;
    try {
        await api.deleteCloudConfig(id);
        await refreshCloudUI();
        toast('云端配置已删除');
    } catch (error) {
        toast(`删除失败：${error.message}`);
    }
}

async function handleCloudExport() {
    const configId = $('export-cloud-config').value;
    if (!configId) { toast('请先选择云端配置（设置 → 云端存储）'); return; }
    const content = currentExportContent();
    if (!content) { toast('没有可导出的内容'); return; }
    const btn = $('btn-export-cloud');
    const status = $('cloud-export-status');
    const urlEl = $('cloud-export-url');
    const resultBox = $('cloud-export-result');
    btn.disabled = true;
    btn.textContent = '上传中…';
    resultBox.hidden = false;
    urlEl.hidden = true;
    urlEl.removeAttribute('href');
    status.textContent = '正在上传至云端…';
    try {
        const extension = exportFormat() === 'csv' ? 'csv' : 'txt';
        const key = $('export-cloud-key').value.trim() || `iptest-result.${extension}`;
        const data = await api.uploadCloud(configId, key, content);
        const url = data.url || '';
        urlEl.href = url;
        urlEl.textContent = url;
        urlEl.hidden = !url;
        const size = Number(data.size) || content.length;
        status.textContent = size >= 1024 * 1024
            ? `已上传 ${(size / 1024 / 1024).toFixed(2)} MB`
            : size >= 1024 ? `已上传 ${(size / 1024).toFixed(1)} KB` : `已上传 ${size} B`;
        toast('已导出至云端');
    } catch (error) {
        status.textContent = `上传失败：${error.message}`;
        toast(`导出至云端失败：${error.message}`);
    } finally {
        btn.textContent = '上传至云端';
        btn.disabled = !currentExportContent() || !$('export-cloud-config').value;
    }
}

function bindCloudSettings() {
    $('btn-cloud-save').addEventListener('click', saveCloudConfig);
    $('btn-cloud-test').addEventListener('click', testCloudConfig);
    $('btn-cloud-cancel').addEventListener('click', closeCloudEditor);
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
        const libraries = data.libraries || [];
        const stats = data.stats || {};
        sel.innerHTML = '<option value="">选择要导入的 IP 库…</option>'
            + libraries.map(l => `<option value="${escapeHTML(l.id)}">${escapeHTML(l.name)}（${Number(stats[l.id]?.total || 0)} 条）</option>`).join('');
        if ($('import-source').value === 'library') $('btn-import-remote').disabled = !sel.value;
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
    bindSettingsAutoSave();
    bindExport();
    bindCloudSettings();
    refreshCloudUI();
    bindEvents();
    renderRawSummary();
    renderCandidates();

    try {
        const config = await api.fetchConfig();
        defaults = config.defaults;
        document.documentElement.dataset.platform = config.platform || 'unknown';
        tasksPage?.setCapabilities?.(config.capabilities || {});
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
