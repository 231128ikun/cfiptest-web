// app.js —— 主控：初始化各模块、编排流水线步骤

import { getInputStats } from './input.js';
import * as api from './api.js';
import {
    store, subscribe, setMode, addToWorkspaceFromText, addToWorkspace,
    clearWorkspace, setWorkspaceFilter, clearWorkspaceFilter,
    appendVisibleToCandidates, clearCandidates,
    visibleWorkspace, candidateTargets, filterIsValid, targetToLine,
} from './store.js';
import { ResultTable, CSV_COLUMNS, GROUP_DIMENSIONS } from './table.js';
import { TABLE_COLUMNS, escapeHTML } from './columns.js';
import { createMultiSelect } from './multiselect.js';
import { PRESETS, formatResults, placeholderNames } from './composer.js';
import { downloadAsText, downloadAsCSV, copyToClipboard } from './exporter.js';

const $ = id => document.getElementById(id);

/* ---------------- 全局状态 ---------------- */
let currentTaskId = null;
let currentTaskType = null; // 'latency' | 'speed'
let eventSource = null;
let table = null;
let defaults = null;
let officialRanges = null; // 已拉取的官方段（{ipv4, ipv6, estimate, source}）
let quotaPicker = null;    // 「只列出分组」的多选控件（createMultiSelect 实例）
let quotaGroups = [];      // 多选里当前选中的分组名；空数组 = 不过滤，列出全部

/* ---------------- Toast ---------------- */
let toastTimer = null;
function toast(msg) {
    const el = $('toast');
    el.textContent = msg;
    el.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.remove('show'), 2200);
}

/* ---------------- 阶段 1：输入来源 ---------------- */

/** 当前抽样设置（官方模式用；也用于远程导入里的 CIDR 展开） */
function sampleSettings() {
    const mode = document.querySelector('input[name="sample-mode"]:checked')?.value || 'one';
    return { sampleMode: mode, sampleN: parseInt($('sample-n').value, 10) || 1 };
}

/** 把 store 的当前状态渲染到阶段 1 的两个框与统计行 */
function renderInputStage() {
    const visible = visibleWorkspace();
    $('ip-workspace').value = visible.map(targetToLine).join('\n');
    $('ip-candidates').value = store.candidates.map(targetToLine).join('\n');
    $('workspace-count').textContent = `${store.workspace.length} 条`;
    $('candidate-count').textContent = `${store.candidates.length} 条`;

    const stats = getInputStats(visible.map(targetToLine));
    $('stat-visible').textContent = visible.length;
    $('stat-valid').textContent = store.workspace.length;
    $('stat-v4').textContent = stats.v4;
    $('stat-v6').textContent = stats.v6;
    const portText = Object.entries(stats.ports)
        .sort((a, b) => b[1] - a[1])
        .slice(0, 6)
        .map(([p, n]) => `${p}×${n}`)
        .join(' ');
    $('stat-ports').innerHTML = portText ? `端口分布 <b>${portText}</b>` : '';

    // 筛选表达式非法时给个视觉提示，但不清空列表（visibleWorkspace 会退回全量）
    $('filter-expr').style.borderColor = filterIsValid() ? '' : 'var(--danger)';

    $('btn-start-latency').disabled = store.candidates.length === 0 || currentTaskId !== null;
}

/**
 * 把一段 IP 文本加入工作区。
 *
 * 不含网段时走前端 store.parseLines（零往返）；含 "/" 时交后端 /api/import/text，
 * 因为 CIDR 的抽样算法只在 engine 里有一份（带单测），不在 JS 里重复实现。
 */
async function addText(rawText, onSuccess) {
    if (!rawText.trim()) { toast('内容为空'); return; }

    if (!rawText.includes('/')) {
        const { added, dupCount, invalidCount } = addToWorkspaceFromText(rawText);
        if (!added && !dupCount) { toast('没有识别到有效 IP'); return; }
        onSuccess?.();
        toast(`已加入 ${added} 条（去重 ${dupCount}，丢弃 ${invalidCount}）`);
        return;
    }

    try {
        const resp = await api.importText(rawText, sampleSettings());
        const { added, dupCount } = addToWorkspace(resp.targets);
        onSuccess?.();
        toast(`已加入 ${added} 条（去重 ${dupCount}，含网段展开）`);
    } catch (e) {
        toast(e.message);
    }
}

function bindInputStage() {
    // ---- 模式 Tab：只切换本阶段的来源面板 ----
    $('mode-tabs').addEventListener('click', e => {
        const tab = e.target.closest('.mode-tab');
        if (!tab) return;
        setMode(tab.dataset.mode);
        document.querySelectorAll('.mode-tab').forEach(t => {
            const on = t.dataset.mode === store.mode;
            t.classList.toggle('active', on);
            t.setAttribute('aria-selected', String(on));
        });
        $('source-proxy').hidden = store.mode !== 'proxy';
        $('source-official').hidden = store.mode !== 'official';
    });

    // ---- 来源一：粘贴 ----
    // 含 "/" 说明混写了 CIDR 网段，交后端展开（抽样算法只在 engine 里有一份）；
    // 纯 IP 列表在前端解析即可，省一次往返。
    $('btn-add-paste').addEventListener('click', () => addText($('ip-input').value, () => {
        $('ip-input').value = '';
    }));

    // ---- 来源二：远程 TXT（后端代取，避开 CORS）----
    $('btn-import-remote').addEventListener('click', async () => {
        const url = $('remote-url').value.trim();
        if (!url) { toast('请填写远程地址'); return; }
        const btn = $('btn-import-remote');
        btn.disabled = true;
        btn.textContent = '导入中…';
        try {
            const resp = await api.importRemote(url, sampleSettings());
            const { added, dupCount } = addToWorkspace(resp.targets);
            toast(`已导入 ${added} 条（去重 ${dupCount}）`);
        } catch (e) {
            toast(e.message);
        } finally {
            btn.disabled = false;
            btn.textContent = '导入远程';
        }
    });

    // ---- 来源三：本地文件（前端读取，网段仍交后端展开）----
    $('file-input').addEventListener('change', async e => {
        const file = e.target.files?.[0];
        if (!file) return;
        try {
            await addText(await file.text());
        } catch (err) {
            toast('读取文件失败：' + err.message);
        }
        e.target.value = ''; // 允许重复选同一个文件
    });

    // ---- 来源四：官方段 ----
    $('btn-fetch-ranges').addEventListener('click', fetchRanges);
    $('btn-add-ranges').addEventListener('click', addOfficialToWorkspace);
    document.querySelectorAll('input[name="sample-mode"]').forEach(radio =>
        radio.addEventListener('change', renderRangesEstimate));
    $('sample-n').addEventListener('input', renderRangesEstimate);

    // ---- 工作区筛选：非破坏性，清除即可回退全量 ----
    $('filter-expr').addEventListener('input', e => setWorkspaceFilter(e.target.value, 'keep'));
    $('btn-filter-keep').addEventListener('click', () => setWorkspaceFilter($('filter-expr').value, 'keep'));
    $('btn-filter-remove').addEventListener('click', () => setWorkspaceFilter($('filter-expr').value, 'remove'));
    $('btn-filter-clear').addEventListener('click', () => {
        $('filter-expr').value = '';
        clearWorkspaceFilter();
    });
    $('btn-workspace-clear').addEventListener('click', () => clearWorkspace());

    // ---- 工作区 → 候选区 ----
    $('btn-append-candidates').addEventListener('click', () => {
        const visible = visibleWorkspace();
        if (!visible.length) { toast('工作区没有可追加的行'); return; }
        const { added, dupCount } = appendVisibleToCandidates();
        toast(`已追加 ${added} 条到候选区（去重 ${dupCount}）`);
    });
    $('btn-candidates-clear').addEventListener('click', () => clearCandidates());
}

async function fetchRanges() {
    const btn = $('btn-fetch-ranges');
    btn.disabled = true;
    try {
        officialRanges = await api.fetchOfficialRanges(sampleSettings().sampleN);
        const src = officialRanges.source === 'builtin' ? '内置兜底' : '官方接口';
        $('ranges-status').textContent =
            `${src}：IPv4 ${officialRanges.ipv4.length} 段 / IPv6 ${officialRanges.ipv6.length} 段`;
        if (officialRanges.warning) toast(officialRanges.warning);
        renderRangesEstimate();
    } catch (e) {
        $('ranges-status').textContent = '拉取失败';
        toast('拉取官方段失败：' + e.message);
    } finally {
        btn.disabled = false;
    }
}

function renderRangesEstimate() {
    if (!officialRanges) return;
    const { sampleMode, sampleN } = sampleSettings();
    const est = officialRanges.estimate || {};
    let count = est.onePerSubnet;
    if (sampleMode === 'n') count = (est.onePerSubnet || 0) * Math.max(1, sampleN);
    else if (sampleMode === 'all') count = est.all;

    const warn = sampleMode === 'all'
        ? '（百万级，不建议）'
        : sampleMode === 'n' && sampleN > 4 ? '（数量较大，耗时明显增加）' : '';
    $('ranges-estimate').textContent = `按此粒度约 ${count?.toLocaleString() ?? '?'} 个 IPv4${warn}`;
}

/**
 * 官方段按当前抽样粒度展开后加入工作区。
 *
 * 端口固定 443（官方模式暂时只需要 443）；后端 ExpandCIDRs 仍带 port 参数，
 * 将来要放开端口选择不必改后端。
 * 全取模式会被后端的 maxExpandedTargets 拦下并给出提示，前端先劝一句。
 */
async function addOfficialToWorkspace() {
    if (!officialRanges) { toast('请先拉取官方段'); return; }
    if (sampleSettings().sampleMode === 'all') {
        toast('全取模式约 152 万个 IP，超出单次导入上限，请改用更粗的抽样粒度');
        return;
    }

    const btn = $('btn-add-ranges');
    btn.disabled = true;
    try {
        // 每行一个网段，端口写死 443，交后端按 sampleMode 抽样
        await addText(officialRanges.ipv4.map(c => `${c}:443`).join('\n'));
    } finally {
        btn.disabled = false;
    }
}

/* ---------------- 阶段 2：测试执行 ---------------- */

function latencyOptions() {
    return {
        maxConcurrency: parseInt($('lat-concurrency').value, 10) || undefined,
        timeoutMs: parseInt($('lat-timeout').value, 10) || undefined,
        maxLatencyMs: parseInt($('lat-maxlatency').value, 10) || 0,
        // 留空与 0 都表示不限制，后端 0=全部测完
        maxResults: parseInt($('lat-maxresults').value, 10) || 0,
        enableTLS: $('lat-tls').checked,
        enableIPAPI: $('lat-ipapi').checked,
    };
}

function speedOptions() {
    return {
        maxConcurrency: parseInt($('spd-concurrency').value, 10) || undefined,
        durationSec: parseInt($('spd-duration').value, 10) || undefined,
        minSpeedKBs: parseFloat($('spd-minspeed').value) || 0,
        // 留空与 0 都表示不限制，后端 0=全部测完
        maxResults: parseInt($('spd-maxresults').value, 10) || 0,
        downloadURL: $('spd-url').value.trim() || undefined,
        enableTLS: $('spd-tls').checked,
    };
}

/** 测速总开关。关闭时阶段 2 的测速面板整体停用，两个测速按钮也一起禁用。 */
function speedEnabled() {
    return $('spd-enable').checked;
}

/**
 * 把总开关状态落到 DOM。
 *
 * 光靠 opacity 不够：视觉上灰了但仍可 focus、可 Tab 进去改值，读屏也听不出禁用。
 * 所以三件事都做——真的 disabled（顺带把元素移出 Tab 序）、aria-disabled、加类名走样式。
 */
function applySpeedEnabled() {
    const on = speedEnabled();
    const panel = $('spd-panel');
    panel.classList.toggle('disabled', !on);
    panel.setAttribute('aria-disabled', String(!on));
    // 只禁用字段里的控件，标题行的开关自己要留着能点
    panel.querySelectorAll('.field input').forEach(el => { el.disabled = !on; });
    refreshButtons();
}

function setRunning(running, type) {
    currentTaskId = running ? currentTaskId : null;
    currentTaskType = running ? type : null;
    $('btn-start-latency').disabled = running || store.candidates.length === 0;
    $('btn-start-speed').disabled = running || !speedEnabled() || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = running || !speedEnabled() || table.getAllResults().length === 0;
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
        const targets = candidateTargets();
        if (!targets.length) { toast('候选区为空，请先从工作区追加'); return; }
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

    $('spd-enable').addEventListener('change', applySpeedEnabled);

    const startSpeed = async useFiltered => {
        if (!speedEnabled()) { toast('下载测速已关闭，请先在测速面板勾选「启用」'); return; }
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
        onDone: (msg, reason) => {
            setRunning(false);
            refreshButtons();
            toast(msg);
            // 三种结束原因都是正常收工，只是措辞不同（reason 由 A3 下发）
            $('progress-label').textContent =
                reason === 'limit' ? '已达到最大结果数' :
                reason === 'stopped' ? '已停止' : '已完成';
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
    $('btn-start-speed').disabled = currentTaskId !== null || !speedEnabled() || table.getSelectedResults().length === 0;
    $('btn-speed-filtered').disabled = currentTaskId !== null || !speedEnabled() || table.getAllResults().length === 0;
    $('btn-append').disabled = table.getSelectedResults().length === 0 && table.getAllResults().length === 0;
}

/* ---------------- 阶段 3：结果表格 ---------------- */

function bindResultsStage() {
    table = new ResultTable($('result-table-container'));

    $('result-filter').addEventListener('input', e => table.setFilter(e.target.value));

    // 勾选变化时刷新按钮。ResultTable 在勾选后派发 selectionchange，
    // 无需再靠 setTimeout 等重绘结束（改造前勾选会重建整表）。
    $('result-table-container').addEventListener('selectionchange', refreshButtons);

    bindSortBar();
    bindQuotaPanel();

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

/**
 * 排序下拉 + 升降序按钮。
 *
 * 两个控件与表头点击共用 table.setSort() 这一个入口，再靠 table 派发的
 * sortchange 事件回填 UI——所以点表头时下拉也会跟着变，不会出现
 * 「表格按延迟排、下拉却还写着 IP」的分裂状态。
 */
function bindSortBar() {
    const sel = $('sort-key');
    sel.innerHTML = TABLE_COLUMNS
        .filter(col => col.sortable)
        .map(col => `<option value="${col.key}">${escapeHTML(col.label)}</option>`)
        .join('');

    sel.addEventListener('change', () => table.setSort(sel.value, table.sortAsc));
    $('btn-sort-dir').addEventListener('click', () => table.setSort(table.sortKey, !table.sortAsc));

    // 回填：唯一改动 UI 的地方，三条入口（下拉 / 按钮 / 表头）都经由它。
    $('result-table-container').addEventListener('sortchange', e => {
        const { key, asc } = e.detail;
        sel.value = key;
        $('btn-sort-dir').textContent = asc ? '▲ 升序' : '▼ 降序';
    });

    sel.value = table.sortKey;
    $('btn-sort-dir').textContent = table.sortAsc ? '▲ 升序' : '▼ 降序';
}

/** 当前选中的分组维度 key（country / asnOrg / dataCenter / ipType）。 */
function currentQuotaDim() {
    return $('quota-dim').value || GROUP_DIMENSIONS[0].key;
}

function bindQuotaPanel() {
    const dimSel = $('quota-dim');
    dimSel.innerHTML = GROUP_DIMENSIONS
        .map(d => `<option value="${d.key}">${escapeHTML(d.label)}</option>`)
        .join('');

    // 「只列出分组」：维度换成「数据中心」时有 300 多个分组，全部铺成配额输入框
    // 没法用，所以先用多选把关注的几个挑出来。不选 = 不过滤。
    quotaPicker = createMultiSelect($('quota-picker'), {
        placeholder: '全部分组',
        onChange: vals => { quotaGroups = vals; renderQuotaGrid(); },
    });

    // 换维度时分组名的含义完全变了（国家名 → ASN 名），旧的选择必须丢掉，
    // 否则「只列出日本」会在 ASN 维度下过滤掉所有分组，界面显示空白。
    dimSel.addEventListener('change', () => {
        quotaGroups = [];
        renderQuotaGrid();
    });

    $('btn-quota-toggle').addEventListener('click', () => {
        const box = $('quota-box');
        const show = !box.classList.contains('active');
        if (show) renderQuotaGrid();
        box.classList.toggle('active', show);
    });

    $('btn-quota-apply').addEventListener('click', () => {
        const dim = currentQuotaDim();
        const quotas = {};
        document.querySelectorAll('#quota-grid .quota-item').forEach(item => {
            const n = parseInt(item.querySelector('input').value, 10);
            if (n > 0) quotas[item.dataset.group] = n;
        });
        table.applyGroupQuotas(dim, Object.keys(quotas).length ? quotas : null);
        refreshButtons();
        toast(Object.keys(quotas).length ? `已按配额选择 ${table.getSelectedResults().length} 条` : '已清除配额选择');
    });

    $('btn-quota-clear').addEventListener('click', () => {
        table.clearSelection();
        refreshButtons();
    });
}

/**
 * 渲染配额输入格。分组名来自结果数据（ASN 组织名里出现引号/尖括号完全正常），
 * 所以插入 HTML 前必须过 escapeHTML——data-group 属性里同样要过，否则一个
 * 双引号就能提前闭合属性。
 */
function renderQuotaGrid() {
    const dim = currentQuotaDim();
    const stats = table.getGroupStats(dim);

    // 同步多选的候选项（带计数），保留仍然存在的已选项。
    quotaPicker?.setItems(stats.map(s => ({
        value: s.name,
        label: s.emoji ? `${s.emoji} ${s.name}` : s.name,
        count: s.count,
    })));

    const keep = new Set(quotaGroups);
    const shown = keep.size ? stats.filter(s => keep.has(s.name)) : stats;

    $('quota-grid').innerHTML = shown.length
        ? shown.map(s => `
            <span class="quota-item" data-group="${escapeHTML(s.name)}">
                ${escapeHTML(s.emoji)} ${escapeHTML(s.name)} <span class="count">(${s.count})</span>
                <input type="number" min="0" max="${s.count}" placeholder="0">
            </span>`).join('')
        : `<span style="color:var(--text-secondary);font-size:12px">${stats.length ? '所选分组不在当前结果中' : '暂无结果'}</span>`;
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

    // store 变化即重绘工作区/候选区，两个框不再有各自的刷新入口
    subscribe(renderInputStage);
    renderInputStage();

    // 必须排在 bindResultsStage() 之后：applySpeedEnabled → refreshButtons 会用到 table
    applySpeedEnabled();

    try {
        const cfg = await api.fetchConfig();
        defaults = cfg.defaults;
        $('app-version').textContent = cfg.version || '未知版本';
        $('lat-concurrency').value = defaults.latency.maxConcurrency;
        $('lat-timeout').value = defaults.latency.timeoutMs;
        $('lat-maxlatency').value = defaults.latency.maxLatencyMs;
        $('lat-maxresults').value = defaults.latency.maxResults;
        $('lat-tls').checked = defaults.latency.enableTLS;
        $('lat-ipapi').checked = defaults.latency.enableIPAPI;
        $('spd-concurrency').value = defaults.speed.maxConcurrency;
        $('spd-duration').value = defaults.speed.durationSec;
        $('spd-minspeed').value = defaults.speed.minSpeedKBs;
        $('spd-maxresults').value = defaults.speed.maxResults;
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
