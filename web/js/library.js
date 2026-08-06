// library.js —— IP 库页：库列表（多库管理）+ 库内容表格 + 手动导入/导出。
// 表格复用工作台结果页的 ResultTable 与 columns.js 列注册表，
// 筛选/排序/自定义展示规则与工作台完全共用同一套逻辑，一次性加载全量数据。
import { escapeHTML } from './columns.js';
import { parseCSVEntries } from './input.js';
import { LIBRARY_COLUMNS, LIBRARY_CSV_COLUMNS } from './columns.js';
import { ResultTable } from './table.js';
import { download, downloadAsCSV, serialize } from './exporter.js';
import { PRESETS } from './composer.js';
import { addQuotaRule, readQuotaRules, clearQuotaEditors } from './quota-rules.js';
import * as api from './api.js';

const $ = id => document.getElementById(id);
const LIB_COLUMNS_KEY = 'iptest.library.columns';

/** Library column defs by key: keeps library-specific rendering when setColumns rebuilds. */
const LIB_COL_BY_KEY = new Map(LIBRARY_COLUMNS.map(c => [c.key, c]));

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
        allEntries: [],   // 全量数据（一次性加载）
        loading: false,
        columnKeys: loadSavedColumnKeys() ?? [...LIB_DEFAULT_VISIBLE],
    };
    let table = null;
    let pendingImport = null; // { entries, invalid, total }

    async function loadLibraries() {
        try {
            const data = await api.fetchLibraries();
            state.libraries = data.libraries || [];
            if (!state.current || !state.libraries.some(l => l.id === state.current.id)) {
                state.current = state.libraries[0] || null;
            }
            renderLibList();
            await loadAllEntries();
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
            const total = state.allEntries.length && l.id === state.current?.id ? state.allEntries.length : null;
            const detail = total != null ? `${total} 条` : '点击切换查看';
            return `<div class="lib-item ${l.id === state.current?.id ? 'selected' : ''}" data-id="${escapeHTML(l.id)}">
                <div class="lib-item-name">${escapeHTML(l.name)}</div>
                <div class="lib-item-stats">${detail}</div>
            </div>`;
        }).join('');
    }

    /** 一次性加载当前库的全部数据。 */
    async function loadAllEntries() {
        if (!state.current) {
            state.allEntries = [];
            renderTable();
            renderCount();
            return;
        }
        state.loading = true;
        renderCount();
        try {
            // 分页拉取全量（单页上限 2000）
            const all = [];
            for (let offset = 0; ; offset += 2000) {
                const data = await api.fetchAutoLibrary({ lib: state.current.id, offset, limit: 2000 });
                all.push(...(data.entries || []));
                if (!data.entries?.length) break; // guard against server total inconsistency
                if (all.length >= (data.total || 0)) break;
            }
            state.allEntries = all;
            renderTable();
            renderSortOptions();
            renderLibList();
            renderCount();
        } catch (error) {
            toast(`加载库数据失败：${error.message}`);
        } finally {
            state.loading = false;
            renderCount();
        }
    }

    function renderTable() {
        if (!table) {
            table = new ResultTable($('lib-table-container'), LIBRARY_COLUMNS, {
                emptyText: '当前库为空 —— 点击「导入」添加 IP 数据',
                noMatchText: '没有匹配当前筛选的条目',
                sortKey: '',
                searchFields: ['ip', 'port', 'country', 'countryCode', 'cityZh', 'city', 'region', 'regionZh', 'dataCenter', 'locCode', 'asn', 'asnOrg', 'ipType', 'ipsType', 'status', 'source', 'firstSeenAt', 'lastCheckedAt', 'checks', 'outboundIP', 'visitScheme', 'tlsVersion', 'sni', 'httpVersion', 'warp', 'gateway', 'rbi', 'kex', 'timestamp'],
            });
            table.container.addEventListener('selectionchange', updateActionStates);
            table.setColumns(state.columnKeys, k => LIB_COL_BY_KEY.get(k));
            table.container.querySelector('table')?.classList.add('library-results');
        }
        table.setResults(state.allEntries);
        if (!state.allEntries.length) {
            // table.render() 通过 rAF 重绘，空行要等重绘后再替换
            requestAnimationFrame(() => {
                const row = table.tbody?.querySelector('.empty-row');
                if (row) {
                    row.innerHTML = `<td colspan="${table.columns.length}">
                        <div class="lib-empty-state">
                            <strong>当前库为空</strong>
                            <span>点击下方按钮，导入第一批 IP 数据</span>
                            <button type="button" id="lib-empty-import" class="secondary-button">导入 IP 数据</button>
                        </div>
                    </td>`;
                    row.querySelector('#lib-empty-import')?.addEventListener('click', openImportModal);
                }
            });
        }        updateActionStates();
    }

    /** 渲染排序下拉框（复用工作台的排序控件模式）。 */
    function renderSortOptions() {
        if (!table) return;
        const sortable = LIBRARY_COLUMNS.filter(c => c.sortable && c.key !== '_sel');
        $('lib-sort-key').innerHTML = '<option value="">默认排序</option>'
            + sortable.map(c => `<option value="${c.key}">${escapeHTML(c.label)}</option>`).join('');
        $('lib-sort-key').value = table.sortKey;
    }

    function renderCount() {
        const visible = table ? table._visibleResults().length : 0;
        const total = state.allEntries.length;
        $('lib-count').textContent = state.loading
            ? '加载中…'
            : state.current ? (visible === total ? `共 ${total} 条` : `共 ${total} 条，匹配 ${visible} 条`) : '';
    }

    function renderColumnOptions() {
        const choices = LIBRARY_COLUMNS.filter(c => c.key !== '_sel');
        $('lib-column-options').innerHTML = choices.map(c => `
            <label class="checkbox"><input type="checkbox" data-key="${c.key}" ${state.columnKeys.includes(c.key) ? 'checked' : ''}> ${escapeHTML(c.label)}</label>`).join('');
        $('lib-column-selected-count').textContent = `已选 ${state.columnKeys.length}/${choices.length}`;
    }

    function applyColumnKeys(keys) {
        if (!keys.length) { toast('至少保留一个显示字段'); renderColumnOptions(); return; }
        state.columnKeys = keys;
        table?.setColumns(keys, k => LIB_COL_BY_KEY.get(k));
        renderColumnOptions();
    }

    /** 根据当前库/数据/勾选状态统一刷新按钮可用性。 */
    function updateActionStates() {
        const hasLib = !!state.current;
        const hasEntries = state.allEntries.length > 0;
        $('lib-import').disabled = !hasLib;
        $('lib-export').disabled = !hasLib;
        $('lib-rename').disabled = !hasLib;
        $('lib-clear').disabled = !hasLib || !hasEntries;
        $('lib-delete').disabled = !hasLib;
        const n = table ? table.getSelectedResults().length : 0;
        $('lib-remove-selected').disabled = n === 0;
        $('lib-selected-count').textContent = n ? `已选 ${n} 条` : '';
        renderCount();
    }

    async function removeSelected() {
        if (!table) return;
        const keys = table.getSelectedResults().map(r => ResultTable.keyOf(r));
        if (!keys.length) { toast('请先勾选要移除的条目'); return; }
        try {
            const result = await api.removeAutoLibrary(state.current.id, keys);
            toast(`已移除 ${result.removed} 条`);
            await loadAllEntries();
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
            await loadAllEntries();
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
            await loadAllEntries();
        } catch (error) {
            toast(`导入失败：${error.message}`);
        }
    }

    // ---- 手动导出（复用 exporter.js 的 serialize / download 与列注册表）----
    function openExportModal() {
        if (!state.current) { toast('请先选择 IP 库'); return; }
        $('lib-export-modal').hidden = false;
    }

    async function confirmExport() {
        if (!state.current) return;
        const format = document.querySelector('input[name="lib-export-format"]:checked')?.value || 'txt';
        try {
            // 导出当前筛选可见的结果（复用工作台的筛选逻辑）
            const rows = table ? table.getAllResults() : state.allEntries;
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

    // ---- 自定义展示规则（复用共享模块）----
    function applyQuotaRules() {
        if (!table) return;
        const rules = readQuotaRules();
        const shown = table.applyDisplayRules(rules);
        toast(rules.length ? `已应用 ${rules.length} 条规则，当前展示 ${shown} 条` : '请至少选择一条规则的值');
        renderCount();
    }

    function clearQuotaRules() {
        if (!table) return;
        table.clearDisplayRules();
        clearQuotaEditors();
        addQuotaRule($('lib-quota-rules'), table);
        renderCount();
    }

    // ---- 事件绑定 ----
    function bindEvents() {
        // 库列表切换
        $('lib-list').addEventListener('click', e => {
            const item = e.target.closest('.lib-item');
            if (!item) return;
            const lib = state.libraries.find(l => l.id === item.dataset.id);
            if (lib && lib.id !== state.current?.id) {
                state.current = lib;
                // 清除展示规则
                clearQuotaEditors();
                if (table) table.clearDisplayRules();
                renderLibList();
                loadAllEntries();
            }
        });

        $('lib-new').addEventListener('click', createNew);

        // 筛选（复用工作台的文本筛选 + ResultTable 客户端筛选）
        let qTimer = null;
        $('lib-filter').addEventListener('input', () => {
            clearTimeout(qTimer);
            qTimer = setTimeout(() => {
                table?.setFilter($('lib-filter').value);
                renderCount();
            }, 150);
        });

        // 排序
        $('lib-sort-key').addEventListener('change', () => {
            const key = $('lib-sort-key').value;
            if (!key) { table?.clearSort(); return; }
            if (!table) return;
            table.setSort(key, table.sortKey === key ? !table.sortAsc : true);
        });
        $('btn-lib-sort-dir').addEventListener('click', () => {
            if (!table) return;
            table.setSort(table.sortKey, !table.sortAsc);
        });
        // header click / dropdown share one sort state (workbench pattern)
        $('lib-table-container').addEventListener('sortchange', event => {
            $('lib-sort-key').value = event.detail.key;
            $('btn-lib-sort-dir').textContent = event.detail.asc ? '▲ 升序' : '▼ 降序';
            renderCount();
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

        // 自定义展示规则
        $('lib-quota-toggle').addEventListener('click', () => {
            const box = $('lib-quota-box');
            const open = !box.classList.contains('active');
            if (open && document.querySelectorAll('#lib-quota-rules .quota-rule').length === 0) {
                addQuotaRule($('lib-quota-rules'), table);
            }
            box.classList.toggle('active', open);
            $('lib-quota-toggle').classList.toggle('active', open);
            $('lib-quota-toggle').setAttribute('aria-expanded', String(open));
        });
        $('btn-lib-quota-add-rule').addEventListener('click', () => {
            addQuotaRule($('lib-quota-rules'), table);
        });
        $('btn-lib-quota-apply').addEventListener('click', applyQuotaRules);
        $('btn-lib-quota-clear').addEventListener('click', clearQuotaRules);

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
            const quotaBox = $('lib-quota-box');
            const quotaToggle = $('lib-quota-toggle');
            if (!moreWrap.contains(e.target)) {
                moreWrap.classList.remove('open');
                $('lib-more').setAttribute('aria-expanded', 'false');
            }
            if (colBox.classList.contains('active') && e.target !== colToggle && !colBox.contains(e.target) && !colToggle.contains(e.target)) {
                colBox.classList.remove('active');
                colToggle.setAttribute('aria-expanded', 'false');
            }
            if (quotaBox?.classList.contains('active') && e.target !== quotaToggle && !quotaBox.contains(e.target) && !quotaToggle?.contains(e.target)) {
                quotaBox.classList.remove('active');
                quotaToggle?.classList.remove('active');
                quotaToggle?.setAttribute('aria-expanded', 'false');
            }
        });
        $('lib-more-wrap').addEventListener('click', () => {
            $('lib-more-wrap').classList.remove('open');
            $('lib-more').setAttribute('aria-expanded', 'false');
        });

        // 更多菜单操作
        $('lib-rename').addEventListener('click', renameCurrent);
        $('lib-remove-selected').addEventListener('click', removeSelected);
        $('lib-clear').addEventListener('click', clearCurrent);
        $('lib-delete').addEventListener('click', deleteCurrent);

        // 导入弹窗：文件选择 + 拖拽 + 预览
        $('lib-import').addEventListener('click', openImportModal);
        $('lib-import-modal').addEventListener('click', e => { if (e.target === $('lib-import-modal')) $('lib-import-modal').hidden = true; });
        $('btn-lib-import-close').addEventListener('click', () => $('lib-import-modal').hidden = true);
        $('btn-lib-import-cancel').addEventListener('click', () => $('lib-import-modal').hidden = true);
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
        $('lib-export').addEventListener('click', openExportModal);
        $('lib-export-modal').addEventListener('click', e => { if (e.target === $('lib-export-modal')) $('lib-export-modal').hidden = true; });
        $('btn-lib-export-close').addEventListener('click', () => $('lib-export-modal').hidden = true);
        $('btn-lib-export-cancel').addEventListener('click', () => $('lib-export-modal').hidden = true);
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
    }

    renderColumnOptions();
    bindEvents();
    loadLibraries();
    return { refresh: loadLibraries };
}
