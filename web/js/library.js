// library.js —— IP 库页：库列表（多库管理）+ 库内容表格 + 手动导入/导出。
// 表格复用工作台结果页的 ResultTable 与 columns.js 列注册表，
// 导出复用 exporter.js 的 serialize / download，避免维护第二套渲染与导出逻辑。
import { escapeHTML } from './columns.js';
import { importCSVText } from './input.js';
import { LIBRARY_COLUMNS, LIBRARY_CSV_COLUMNS } from './columns.js';
import { ResultTable } from './table.js';
import { download, downloadAsCSV, serialize } from './exporter.js';
import { PRESETS } from './composer.js';
import * as api from './api.js';

const $ = id => document.getElementById(id);
const PAGE_SIZE = 2000; // 后端单页上限

export function initLibrary({ toast }) {
    const state = {
        libraries: [],
        current: null, // Info
        entries: [],
        total: 0,
        stats: null,
    };
    let table = null;

    async function loadLibraries() {
        try {
            const data = await api.fetchLibraries();
            state.libraries = data.libraries || [];
            if (!state.current || !state.libraries.some(l => l.id === state.current.id)) {
                state.current = state.libraries[0] || null;
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
            wrap.innerHTML = '<div class="task-detail-empty">暂无 IP 库</div>';
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

    const FIELD_PARAM = { country: 'country', city: 'city', dc: 'dc', asn: 'asn', port: 'port' };

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
            $('lib-count').textContent = '';
            return;
        }
        try {
            const data = await api.fetchAutoLibrary({
                lib: state.current.id,
                status: $('lib-status').value,
                q: $('lib-q').value.trim(),
                limit: 500,
                ...currentFilterParams(),
            });
            state.entries = data.entries || [];
            state.total = data.total || 0;
            state.stats = { ...(state.stats || {}), [state.current.id]: data.stats };
            renderLibList();
            renderStats();
            renderFieldValueOptions(data.stats);
            renderTable();
        } catch (error) {
            toast(`加载库内容失败：${error.message}`);
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
            sel.innerHTML = '<option value="">选择取值…</option>';
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
        const keys = Object.keys(map).sort();
        sel.disabled = keys.length === 0;
        sel.innerHTML = ['<option value="">全部</option>']
            .concat(keys.map(k => `<option value="${escapeHTML(k)}">${escapeHTML(k)}（${map[k]}）</option>`))
            .join('');
        sel.value = current;
    }

    function renderTable() {
        if (!table) {
            table = new ResultTable($('lib-table-container'), LIBRARY_COLUMNS);
            table.container.querySelector('table')?.classList.add('library-results');
            table.container.addEventListener('selectionchange', () => {
                const n = table.getSelectedResults().length;
                $('lib-remove-selected').disabled = n === 0;
                $('lib-selected-count').textContent = n ? `已选 ${n} 条` : '';
            });
        }
        table.clear();
        state.entries.forEach(e => table.appendResult(e));
        $('lib-remove-selected').disabled = true;
        $('lib-selected-count').textContent = '';
        $('lib-count').textContent = state.current ? `共 ${state.total} 条，当前显示 ${state.entries.length} 条` : '';
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

    // ---- 手动导入（复用后端 /api/auto/library/import 的解析逻辑）----
    function openImportModal() {
        if (!state.current) { toast('请先选择 IP 库'); return; }
        $('lib-import-text').value = '';
        $('lib-import-modal').hidden = false;
    }

    /** 识别 CSV：首行含 IP/Port 表头，或首行就是「IP,端口」数据行。 */
    function isCSVText(text) {
        const first = text.split(/\r?\n/).find(line => line.trim());
        if (!first || !first.includes(',')) return false;
        const lower = first.toLowerCase();
        if ((lower.includes('ip') || lower.includes('ip地址')) && (lower.includes('port') || lower.includes('端口'))) return true;
        const cells = first.split(',');
        if (cells.length < 2) return false;
        const ip = cells[0].trim().replace(/^\[|\]$/g, '');
        const ipv4 = /^\d{1,3}(\.\d{1,3}){3}$/.test(ip);
        const ipv6 = /^[0-9a-fA-F:]{2,}$/.test(ip) && ip.includes(':');
        return (ipv4 || ipv6) && /^\d{1,5}$/.test(cells[1].trim());
    }
    async function confirmImport() {
        let text = $('lib-import-text').value.trim();
        if (!text) { toast('请先粘贴 IP 文本或选择文件'); return; }
        if (isCSVText(text)) {
            const converted = importCSVText(text);
            if (!converted) { toast('CSV 内容无法识别，请确认包含 IP、端口列'); return; }
            text = converted;
        }
        try {
            const resp = await api.importAutoLibrary({
                lib: state.current.id,
                text,
                sampleMode: $('lib-import-sample-mode').value,
                sampleN: Number($('lib-import-sample-n').value) || 1,
            });
            toast(`已导入「${state.current.name}」：新增 ${resp.added} 条，更新 ${resp.updated} 条（共 ${resp.total} 条）`);
            $('lib-import-modal').hidden = true;
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
        const params = { lib: state.current.id, status: $('lib-status').value, q: $('lib-q').value.trim(), limit: PAGE_SIZE };
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

    // 事件绑定
    $('lib-list').addEventListener('click', e => {
        const item = e.target.closest('.lib-item');
        if (!item) return;
        state.current = state.libraries.find(l => l.id === item.dataset.id) || null;
        renderLibList();
        loadEntries();
    });
    $('lib-new').addEventListener('click', createNew);
    $('lib-import').addEventListener('click', openImportModal);
    $('lib-export').addEventListener('click', openExportModal);
    $('lib-rename').addEventListener('click', renameCurrent);
    $('lib-remove-selected').addEventListener('click', removeSelected);
    $('lib-clear').addEventListener('click', clearCurrent);
    $('lib-delete').addEventListener('click', deleteCurrent);
    $('lib-field').addEventListener('change', () => { renderFieldValueOptions(state.stats?.[state.current?.id]); loadEntries(); });
    $('lib-field-value').addEventListener('change', loadEntries);
    ['lib-q', 'lib-status'].forEach(id => $(id).addEventListener('input', loadEntries));
    $('lib-status').addEventListener('change', loadEntries);

    // 导入弹窗
    $('lib-import-modal').addEventListener('click', e => { if (e.target === $('lib-import-modal')) $('lib-import-modal').hidden = true; });
    $('btn-lib-import-close').addEventListener('click', () => { $('lib-import-modal').hidden = true; });
    $('btn-lib-import-cancel').addEventListener('click', () => { $('lib-import-modal').hidden = true; });
    $('btn-lib-import-confirm').addEventListener('click', confirmImport);
    $('lib-import-file').addEventListener('change', async event => {
        const file = event.target.files?.[0];
        if (!file) return;
        const text = await file.text();
        $('lib-import-text').value = text;
        toast(`已读取 ${file.name}（${text.length} 字符），确认后导入`);
        event.target.value = '';
    });
    $('lib-import-sample-mode').addEventListener('change', () => {
        $('lib-import-sample-n').disabled = $('lib-import-sample-mode').value !== 'n';
    });

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

    loadLibraries();
    return { refresh: loadLibraries };
}
