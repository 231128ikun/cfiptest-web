// library.js —— IP 库页：库列表（多库管理）+ 库内容表格 + 手动导入/导出。
// 表格复用工作台结果页的 ResultTable 与 columns.js 列注册表，
// 导出复用 exporter.js 的 serialize / download，避免维护第二套渲染与导出逻辑。
import { escapeHTML } from './columns.js';
import { parseCSVEntries } from './input.js';
import { LIBRARY_COLUMNS, LIBRARY_CSV_COLUMNS } from './columns.js';
import { ResultTable } from './table.js';
import { download, downloadAsCSV, serialize } from './exporter.js';
import { PRESETS } from './composer.js';
import * as api from './api.js';

const $ = id => document.getElementById(id);
const PAGE_SIZE = 500; // 前端分页每页条数（后端单页上限 2000）
const LIB_COLUMNS_KEY = 'iptest.library.columns';

/** 库页默认显示列（其余字段可在「显示字段」里勾选）。 */
const LIB_DEFAULT_VISIBLE = ['ip', 'port', 'dataCenter', 'country', 'cityZh', 'status', 'tcpLatencyMs', 'downloadSpeedKBs', 'lastCheckedAt', 'checks', 'source'];

/** 从 localStorage 读取用户自选的显示列；无记录或记录无效时返回 null（用默认列）。 */
function loadSavedColumnKeys() {
    try {
        const raw = localStorage.getItem(LIB_COLUMNS_KEY);
        if (!raw) return null;
        const keys = JSON.parse(raw);
        const valid = new Set(LIBRARY_COLUMNS.filter(c => c.key !== '_sel').map(c => c.key));
        const filtered = (Array.isArray(keys) ? keys : []).filter(k => valid.has(k));
        return filtered.length ? filtered : null;
    } catch { return null; }
}

export function initLibrary({ toast }) {
    const state = {
        libraries: [],
        current: null, // Info
        entries: [],
        total: 0,
        stats: null,
        page: 0,
        loading: false,
        columnKeys: loadSavedColumnKeys() ?? [...LIB_DEFAULT_VISIBLE],
    };
    let table = null;
    let qTimer = null;
    let pendingImport = null; // { entries, invalid, total }

    async function loadLibraries() {
        try {
            const data = await api.fetchLibraries();
            state.libraries = data.libraries || [];
            if (!state.current || !state.libraries.some(l => l.id === state.current.id)) {
                state.current = state.libraries[0] || null;
                state.page = 0;
            }
            renderLibList();
            await loadEntries();
        } catch (error) {
            toast(`加载 IP 库失败：${error.message}`);
        }
    }

    function renderLibList() {
        const wrap = $('lib-list');
        if (!state.libraries.length) {
            wrap.innerHTML = '<div class="task-detail-empty">暂无 IP 库，点击「新建库」创建</div>';
            return;
        }
        wrap.innerHTML = state.libraries.map(l => {
            const st = state.stats?.[l.id];
            const detail = st ? `${st.total} 条（有效 ${st.active} / 未测 ${st.new}）` : '加载中…';
            return `<div class="lib-item ${l.id === state.current?.id ? 'selected' : ''}" data-id="${escapeHTML(l.id)}">
                <div class="lib-item-name">${escapeHTML(l.name)}</div>
                <div class="lib-item-stats">${detail}</div>
            </div>`;
        }).join('');
    }

    const FIELD_PARAM = { country: 'country', city: 'city', dc: 'dc', asn: 'asn', port: 'port', status: 'status' };

    function currentFilterParams() {
        const params = {};
        const field = $('lib-field').value;
        const fieldValue = $('lib-field-value').value;
        if (field && fieldValue) params[FIELD_PARAM[field] || field] = fieldValue;
        return params;
    }

    async function loadEntries() {
        if (!state.current) {
            state.entries = [];
            state.total = 0;
            renderTable();
            renderCount();
            renderPager();
            return;
        }
        state.loading = true;
        renderCount();
        renderPager();
        try {
            const data = await api.fetchAutoLibrary({
                lib: state.current.id,
                q: $('lib-q').value.trim(),
                offset: state.page * PAGE_SIZE,
                limit: PAGE_SIZE,
                ...currentFilterParams(),
            });
            state.entries = data.entries || [];
            state.total = data.total || 0;
            // 当前页越界（如删除条目后）时自动回退一页
            const pages = Math.max(1, Math.ceil(state.total / PAGE_SIZE));
            if (state.page >= pages) {
                state.page = Math.max(0, pages - 1);
                return loadEntries();
            }
            state.stats = { ...(state.stats || {}), [state.current.id]: data.stats };
            renderLibList();
            renderStats();
            renderFieldValueOptions(data.stats);
            renderTable();
        } catch (error) {
            toast(`加载库内容失败：${error.message}`);
        } finally {
            state.loading = false;
            renderCount();
            renderPager();
        }
    }

    function renderStats() {
        const st = state.current ? state.stats?.[state.current.id] : null;
        $('lib-stats').textContent = st
            ? `${escapeHTML(state.current.name)}：共 ${st.total} 条 · 有效 ${st.active} · 未测 ${st.new} · 已测速 ${st.speedValid}`
            : '';
    }

    function renderFieldValueOptions(stats) {
        const field = $('lib-field').value;
        const sel = $('lib-field-value');
        if (!field) {
            sel.disabled = true;
            sel.innerHTML = '<option value="">全部取值</option>';
            return;
        }
        const map = {
            country: stats?.byCountry,
            city: stats?.byCity,
            dc: stats?.byDC,
            asn: stats?.byASN,
            port: stats?.byPort,
        }[field] || {};
        const current = sel.value;
        if (field === 'status') {
            const label = { active: '有效', new: '未测' }; // 有效 / 未测
            sel.disabled = false;
            sel.innerHTML = ['<option value="">全部取值</option>']
                .concat(['active', 'new'].map(k => `<option value="${k}">${label[k]}（${stats?.[k] ?? 0}）</option>`))
                .join('');
        } else {
            const keys = Object.keys(map).sort();
            sel.disabled = keys.length === 0;
            sel.innerHTML = ['<option value="">全部取值</option>']
                .concat(keys.map(k => `<option value="${escapeHTML(k)}">${escapeHTML(k)}（${map[k]}）</option>`))
                .join('');
        }
        sel.value = current;
    }

    function renderTable() {
        if (!table) {
            table = new ResultTable($('lib-table-container'), LIBRARY_COLUMNS, {
                emptyText: '当前库为空 —— 点击「导入」添加 IP 数据',
                noMatchText: '没有匹配当前筛选的条目',
            });
            table.container.addEventListener('selectionchange', updateActionStates);
            table.setColumns(state.columnKeys);
            table.container.querySelector('table')?.classList.add('library-results');
        }
        table.clear();
        state.entries.forEach(e => table.appendResult(e));
        if (!state.entries.length) {
            const row = table.tbody?.querySelector('.empty-row');
            if (row) {
                const isEmpty = state.total === 0;
                row.innerHTML = `<td colspan="${table.columns.length}">
                    <div class="lib-empty-state">
                        <strong>${isEmpty ? '当前库为空' : '没有匹配当前筛选的条目'}</strong>
                        <span>${isEmpty ? '点击下方按钮，导入第一批 IP 数据' : '试试调整搜索或筛选条件'}</span>
                        ${isEmpty ? '<button type="button" id="lib-empty-import" class="secondary-button">导入 IP 数据</button>' : ''}
                    </div>
                </td>`;
                row.querySelector('#lib-empty-import')?.addEventListener('click', openImportModal);
            }
        }
        updateActionStates();
    }

    function renderCount() {
        $('lib-count').textContent = state.loading
            ? '加载中…'
            : state.current ? `共 ${state.total} 条，当前显示 ${state.entries.length} 条` : '';
    }

    function renderPager() {
        const pager = $('lib-pager');
        if (!state.current) { pager.hidden = true; return; }
        const pages = Math.max(1, Math.ceil(state.total / PAGE_SIZE));
        pager.hidden = pages <= 1 && !state.loading;
        $('lib-page-info').textContent = `第 ${state.page + 1} / ${pages} 页 · 每页 ${PAGE_SIZE} 条`;
        $('lib-page-prev').disabled = state.loading || state.page <= 0;
        $('lib-page-next').disabled = state.loading || state.page >= pages - 1;
    }

    /** 根据当前库/数据/勾选状态统一刷新按钮可用性，避免危险操作误触。 */
    function updateActionStates() {
        const hasLib = !!state.current;
        const hasEntries = state.total > 0;
        $('lib-import').disabled = !hasLib;
        $('lib-export').disabled = !hasLib;
        $('lib-rename').disabled = !hasLib;
        $('lib-clear').disabled = !hasLib || !hasEntries;
        $('lib-delete').disabled = !hasLib;
        const n = table ? table.getSelectedResults().length : 0;
        $('lib-remove-selected').disabled = n === 0;
        $('lib-selected-count').textContent = n ? `已选 ${n} 条` : '';
    }

    async function removeSelected() {
        if (!table) return;
        const keys = table.getSelectedResults().map(r => ResultTable.keyOf(r));
        if (!keys.length) { toast('请先勾选要移除的条目'); return; }
        try {
            const result = await api.removeAutoLibrary(state.current.id, keys);
            toast(`已移除 ${result.removed} 条`);
            await loadEntries();
        } catch (error) {
            toast(`移除失败：${error.message}`);
        }
    }

    async function clearCurrent() {
        if (!state.current) return;
        if (!confirm(`确认清空「${state.current.name}」的全部条目？此操作不可恢复。`)) return;
        try {
            await api.clearLibrary(state.current.id);
            toast('库已清空');
            window.dispatchEvent(new CustomEvent('library-changed'));
            await loadEntries();
        } catch (error) {
            toast(`清空失败：${error.message}`);
        }
    }

    async function renameCurrent() {
        if (!state.current) return;
        const name = prompt('新的库名称', state.current.name);
        if (!name || name.trim() === state.current.name) return;
        try {
            await api.renameLibrary(state.current.id, name.trim());
            toast('库已改名');
            window.dispatchEvent(new CustomEvent('library-changed'));
            await loadLibraries();
        } catch (error) {
            toast(`改名失败：${error.message}`);
        }
    }

    async function deleteCurrent() {
        if (!state.current) return;
        if (!confirm(`确认删除 IP 库「${state.current.name}」？库文件将被移除。`)) return;
        try {
            await api.deleteLibrary(state.current.id);
            toast('库已删除');
            state.current = null;
            window.dispatchEvent(new CustomEvent('library-changed'));
            await loadLibraries();
        } catch (error) {
            toast(`删除失败：${error.message}`);
        }
    }

    async function createNew() {
        const name = prompt('新 IP 库名称', '新库');
        if (!name) return;
        try {
            const data = await api.createLibrary(name.trim());
            toast(`已创建「${data.library.name}」`);
            state.current = data.library;
            await loadLibraries();
        } catch (error) {
            toast(`创建失败：${error.message}`);
        }
    }

    // ---- 手动导入（CSV 全字段：含元数据行走 results 路径，纯 IP:端口行走 targets 路径）----
    function openImportModal() {
        if (!state.current) { toast('请先选择 IP 库'); return; }
        pendingImport = null;
        $('lib-import-file-name').textContent = '';
        $('lib-import-preview').textContent = '';
        $('lib-import-modal').hidden = false;
    }

    async function handleImportFile(file) {
        if (!file) return;
        try {
            const text = await file.text();
            const parsed = parseCSVEntries(text);
            pendingImport = parsed;
            $('lib-import-file-name').textContent = file.name;
            $('lib-import-preview').textContent = parsed.entries.length
                ? `解析到 ${parsed.entries.length} 条（无效 ${parsed.invalid} 条），确认后导入「${state.current.name}」`
                : '未解析到有效条目，请确认 CSV 包含 IP、端口列（或表头为 IP/Port）';
            toast(`已读取 ${file.name}`);
        } catch (error) {
            pendingImport = null;
            $('lib-import-preview').textContent = `读取文件失败：${error.message}`;
        }
    }

    async function confirmImport() {
        if (!state.current) { toast('请先选择 IP 库'); return; }
        if (!pendingImport || !pendingImport.entries.length) { toast('请先选择 CSV 文件'); return; }
        const { entries } = pendingImport;
        const plain = entries.filter(e => Object.keys(e).length <= 2); // 仅 ip/port
        const withMeta = entries.filter(e => Object.keys(e).length > 2);
        try {
            const resp = withMeta.length
                ? await api.importAutoLibrary({ lib: state.current.id, results: withMeta })
                : await api.importAutoLibrary({ lib: state.current.id, targets: plain.map(e => ({ ip: e.ip, port: e.port })) });
            toast(`已导入「${state.current.name}」：新增 ${resp.added} 条，更新 ${resp.updated} 条（共 ${resp.total} 条）`);
            $('lib-import-modal').hidden = true;
            pendingImport = null;
            window.dispatchEvent(new CustomEvent('library-changed'));
            await loadEntries();
        } catch (error) {
            toast(`导入失败：${error.message}`);
        }
    }

    // ---- 手动导出（复用 exporter.js 的 serialize / download 与列注册表）----
    function openExportModal() {
        if (!state.current) { toast('请先选择 IP 库'); return; }
        $('lib-export-modal').hidden = false;
    }

    /** 分页拉取当前筛选条件下的全部条目（单页上限 2000）。 */
    async function fetchAllFiltered() {
        const params = { lib: state.current.id, q: $('lib-q').value.trim(), limit: PAGE_SIZE };
        Object.assign(params, currentFilterParams());
        const all = [];
        for (let offset = 0; ; offset += PAGE_SIZE) {
            const data = await api.fetchAutoLibrary({ ...params, offset });
            all.push(...(data.entries || []));
            if (all.length >= (data.total || 0)) break;
        }
        return all;
    }

    async function confirmExport() {
        if (!state.current) return;
        const format = document.querySelector('input[name="lib-export-format"]:checked')?.value || 'txt';
        try {
            const rows = await fetchAllFiltered();
            if (!rows.length) { toast('当前筛选没有可导出的条目'); return; }
            const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-');
            const base = `${state.current.name}-${stamp}`;
            if (format === 'csv') {
                downloadAsCSV(rows, LIBRARY_CSV_COLUMNS, { filename: `${base}.csv` });
            } else {
                const template = $('lib-export-template').value || '{ip}:{port}';
                const content = serialize(rows, 'txt', { template });
                download(content, `${base}.txt`, 'text/plain;charset=utf-8');
            }
            toast(`已导出 ${rows.length} 条`);
        } catch (error) {
            toast(`导出失败：${error.message}`);
        }
    }

    // ---- 显示字段（列选择器，localStorage 持久化）----
    function renderColumnOptions() {
        const choices = LIBRARY_COLUMNS.filter(c => c.key !== '_sel');
        $('lib-column-options').innerHTML = choices.map(c =>
            `<label class="checkbox"><input type="checkbox" data-key="${c.key}" ${state.columnKeys.includes(c.key) ? 'checked' : ''}> ${escapeHTML(c.label)}</label>`
        ).join('');
        $('lib-column-selected-count').textContent = `已选 ${state.columnKeys.length}/${choices.length}`;
    }

    function applyColumnKeys(keys) {
        const unique = [...new Set(keys)];
        if (!unique.length) { toast('至少保留一个显示字段'); renderColumnOptions(); return; }
        state.columnKeys = unique;
        try { localStorage.setItem(LIB_COLUMNS_KEY, JSON.stringify(unique)); } catch { /* 忽略存储失败 */ }
        table?.setColumns(unique);
        table?.container.querySelector('table')?.classList.add('library-results');
        renderColumnOptions();
        $('lib-column-save-status').textContent = '已更新';
    }

    // 事件绑定
    $('lib-list').addEventListener('click', e => {
        const item = e.target.closest('.lib-item');
        if (!item) return;
        state.current = state.libraries.find(l => l.id === item.dataset.id) || null;
        state.page = 0;
        renderLibList();
        loadEntries();
    });
    $('lib-new').addEventListener('click', createNew);
    $('lib-new-side').addEventListener('click', createNew);
    $('lib-import').addEventListener('click', openImportModal);
    $('lib-export').addEventListener('click', openExportModal);
    $('lib-rename').addEventListener('click', renameCurrent);
    $('lib-remove-selected').addEventListener('click', removeSelected);
    $('lib-clear').addEventListener('click', clearCurrent);
    $('lib-delete').addEventListener('click', deleteCurrent);
    $('lib-field').addEventListener('change', () => { renderFieldValueOptions(state.stats?.[state.current?.id]); state.page = 0; loadEntries(); });
    $('lib-field-value').addEventListener('change', () => { state.page = 0; loadEntries(); });
    // 搜索输入防抖 300ms，避免每次按键都打一次后端
    $('lib-q').addEventListener('input', () => {
        clearTimeout(qTimer);
        qTimer = setTimeout(() => { state.page = 0; loadEntries(); }, 300);
    });

    // 分页
    $('lib-page-prev').addEventListener('click', () => {
        if (state.page > 0 && !state.loading) { state.page--; loadEntries(); }
    });
    $('lib-page-next').addEventListener('click', () => {
        const pages = Math.max(1, Math.ceil(state.total / PAGE_SIZE));
        if (state.page < pages - 1 && !state.loading) { state.page++; loadEntries(); }
    });

    // 显示字段
    $('lib-column-toggle').addEventListener('click', () => {
        const box = $('lib-column-box');
        const active = box.classList.toggle('active');
        $('lib-column-toggle').setAttribute('aria-expanded', String(active));
    });
    $('btn-lib-column-all').addEventListener('click', () => {
        document.querySelectorAll('#lib-column-options input').forEach(i => i.checked = true);
        applyColumnKeys([...document.querySelectorAll('#lib-column-options input:checked')].map(i => i.dataset.key));
    });
    $('btn-lib-column-default').addEventListener('click', () => {
        applyColumnKeys([...LIB_DEFAULT_VISIBLE]);
        $('lib-column-save-status').textContent = '已恢复默认';
    });
    $('lib-column-options').addEventListener('change', () => {
        applyColumnKeys([...document.querySelectorAll('#lib-column-options input:checked')].map(i => i.dataset.key));
    });

    // 「更多」菜单：点击外部关闭
    $('lib-more').addEventListener('click', e => {
        e.stopPropagation();
        const open = $('lib-more-wrap').classList.toggle('open');
        $('lib-more').setAttribute('aria-expanded', String(open));
    });
    document.addEventListener('click', e => {
        const moreWrap = $('lib-more-wrap');
        const colBox = $('lib-column-box');
        const colToggle = $('lib-column-toggle');
        if (!moreWrap.contains(e.target)) {
            moreWrap.classList.remove('open');
            $('lib-more').setAttribute('aria-expanded', 'false');
        }
        if (colBox.classList.contains('active') && e.target !== colToggle && !colBox.contains(e.target) && !colToggle.contains(e.target)) {
            colBox.classList.remove('active');
            colToggle.setAttribute('aria-expanded', 'false');
        }
    });
    // 菜单内点击任意操作后收起「更多」菜单
    $('lib-more-wrap').addEventListener('click', () => {
        $('lib-more-wrap').classList.remove('open');
        $('lib-more').setAttribute('aria-expanded', 'false');
    });

    // 导入弹窗：文件选择 + 拖拽 + 预览
    $('lib-import-modal').addEventListener('click', e => { if (e.target === $('lib-import-modal')) $('lib-import-modal').hidden = true; });
    $('btn-lib-import-close').addEventListener('click', () => { $('lib-import-modal').hidden = true; });
    $('btn-lib-import-cancel').addEventListener('click', () => { $('lib-import-modal').hidden = true; });
    $('btn-lib-import-confirm').addEventListener('click', confirmImport);
    $('lib-import-file').addEventListener('change', async event => {
        const file = event.target.files?.[0];
        event.target.value = '';
        await handleImportFile(file);
    });
    const dropzone = $('lib-import-dropzone');
    if (dropzone) {
        ['dragover', 'dragenter'].forEach(ev => dropzone.addEventListener(ev, e => { e.preventDefault(); dropzone.classList.add('dragover'); }));
        ['dragleave', 'drop'].forEach(ev => dropzone.addEventListener(ev, e => { e.preventDefault(); dropzone.classList.remove('dragover'); }));
        dropzone.addEventListener('drop', async e => {
            const file = e.dataTransfer?.files?.[0];
            await handleImportFile(file);
        });
    }

    // 导出弹窗
    $('lib-export-modal').addEventListener('click', e => { if (e.target === $('lib-export-modal')) $('lib-export-modal').hidden = true; });
    $('btn-lib-export-close').addEventListener('click', () => { $('lib-export-modal').hidden = true; });
    $('btn-lib-export-cancel').addEventListener('click', () => { $('lib-export-modal').hidden = true; });
    $('btn-lib-export-confirm').addEventListener('click', confirmExport);
    document.querySelectorAll('input[name="lib-export-format"]').forEach(input =>
        input.addEventListener('change', () => {
            const isTxt = input.value === 'txt';
            const section = $('lib-export-template-section');
            if (section) section.style.display = isTxt ? '' : 'none';
        }));
    // 导出模板预设（复用测速工作台第三步的 PRESETS）
    const presetSel = $('lib-export-preset');
    if (presetSel) {
        const group = presetSel.querySelector('optgroup');
        if (group) group.innerHTML = PRESETS.map((preset, i) => `<option value="${i}">${preset.name}</option>`).join('');
        presetSel.addEventListener('change', () => {
            const preset = PRESETS[Number(presetSel.value)];
            if (preset) $('lib-export-template').value = preset.template;
        });
    }

    renderColumnOptions();
    loadLibraries();
    return { refresh: loadLibraries };
}