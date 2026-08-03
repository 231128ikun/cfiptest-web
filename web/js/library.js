// library.js —— IP 库页：库列表（多库管理）+ 库内容表格 + 导入/移除/清空/改名/删除
import { escapeHTML } from './columns.js';
import * as api from './api.js';

const $ = id => document.getElementById(id);
const STATUS_LABEL = { active: '有效', new: '未测' };
const fmtNumber = value => Number.isFinite(Number(value)) && Number(value) > 0
    ? Number(value).toLocaleString('zh-CN', { maximumFractionDigits: 0 })
    : '—';
const fmtValue = value => value == null || value === '' ? '—' : String(value);
const fmtTimestamp = value => value ? String(value) : '—';
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

export function initLibrary({ toast }) {
    const state = {
        libraries: [],
        current: null, // Info
        entries: [],
        total: 0,
        stats: null,
        selected: new Set(),
    };

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

    async function loadEntries() {
        const sel = $('lib-status').value;
        const q = $('lib-q').value.trim();
        const field = $('lib-field').value;
        const fieldValue = $('lib-field-value').value;
        const params = {};
        if (field && fieldValue) params[FIELD_PARAM[field] || field] = fieldValue;
        if (!state.current) {
            state.entries = [];
            state.total = 0;
            renderTable();
            return;
        }
        try {
            const data = await api.fetchAutoLibrary({ lib: state.current.id, status: sel, q, limit: 500, ...params });
            state.entries = data.entries || [];
            state.total = data.total || 0;
            state.stats = { ...(state.stats || {}), [state.current.id]: data.stats };
            state.selected.clear();
            renderTable();
            renderLibList();
            renderStats();
            renderFieldValueOptions(data.stats);
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

    const FIELD_LABEL = { country: '国家', city: '城市', dc: '数据中心', asn: 'ASN', port: '端口' };

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
        const tbody = $('lib-tbody');
        const columnCount = 33;
        if (!state.entries.length) {
            tbody.innerHTML = `<tr class="pad"><td colspan="${columnCount}" class="auto-lib-empty">${state.current ? '库为空：粘贴 IP 到上方导入，或在检测结果页一键导入' : '请先选择 IP 库'}</td></tr>`;
            $('lib-count').textContent = state.current ? `共 ${state.total} 条` : '';
            return;
        }
        tbody.innerHTML = state.entries.map(e => {
            const key = `${e.ip}|${e.port || 0}`;
            const checked = state.selected.has(key) ? 'checked' : '';
            const speed = Number(e.downloadSpeedKBs ?? e.speedKBs);
            const speedText = e.speedValid && speed > 0 ? `${fmtNumber(speed)} kB/s` : '—';
            const cell = value => escapeHTML(fmtValue(value));
            return `<tr>
                <td><input type="checkbox" class="lib-check" data-key="${escapeHTML(key)}" ${checked} aria-label="选择 ${escapeHTML(e.ip)}"></td>
                <td class="mono">${cell(e.ip)}</td>
                <td class="num">${e.port || '—'}</td>
                <td>${cell(e.dataCenter)}</td>
                <td>${cell(e.locCode)}</td>
                <td>${cell(e.region)}</td>
                <td>${cell(e.city)}</td>
                <td>${cell(e.regionZh)}</td>
                <td>${cell(e.country)}</td>
                <td>${cell(e.countryCode)}</td>
                <td>${cell(e.cityZh)}</td>
                <td>${cell(e.emoji)}</td>
                <td class="num">${e.tcpLatencyMs > 0 ? `${e.tcpLatencyMs} ms` : '—'}</td>
                <td class="num">${speedText}</td>
                <td>${cell(e.outboundIP)}</td>
                <td>${cell(e.ipType)}</td>
                <td>${cell(e.ipsType)}</td>
                <td class="num">${e.asn || '—'}</td>
                <td>${cell(e.asnOrg)}</td>
                <td>${cell(e.visitScheme)}</td>
                <td>${cell(e.tlsVersion)}</td>
                <td>${cell(e.sni)}</td>
                <td>${cell(e.httpVersion)}</td>
                <td>${cell(e.warp)}</td>
                <td>${cell(e.gateway)}</td>
                <td>${cell(e.rbi)}</td>
                <td>${cell(e.kex)}</td>
                <td>${cell(e.timestamp)}</td>
                <td>${STATUS_LABEL[e.status] || cell(e.status)}</td>
                <td>${fmtTimestamp(e.firstSeenAt)}</td>
                <td>${fmtTimestamp(e.lastCheckedAt)}</td>
                <td class="num">${e.checks || 0}</td>
                <td>${cell(e.source)}</td>
            </tr>`;
        }).join('');
        $('lib-count').textContent = `共 ${state.total} 条，当前显示 ${state.entries.length} 条`;
    }
    async function removeSelected() {
        const keys = [...state.selected];
        if (!keys.length) { toast('请先勾选要移除的条目'); return; }
        try {
            const result = await api.removeAutoLibrary(state.current.id, keys);
            toast(`已移除 ${result.removed} 条`);
            state.selected.clear();
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

    // 事件绑定
    $('lib-list').addEventListener('click', e => {
        const item = e.target.closest('.lib-item');
        if (!item) return;
        state.current = state.libraries.find(l => l.id === item.dataset.id) || null;
        state.selected.clear();
        renderLibList();
        loadEntries();
    });
    $('lib-new').addEventListener('click', createNew);
    $('lib-remove-selected').addEventListener('click', removeSelected);
    $('lib-clear').addEventListener('click', clearCurrent);
    $('lib-rename').addEventListener('click', renameCurrent);
    $('lib-delete').addEventListener('click', deleteCurrent);
    $('lib-field').addEventListener('change', () => { renderFieldValueOptions(state.stats?.[state.current?.id]); loadEntries(); });
    $('lib-field-value').addEventListener('change', loadEntries);
    ['lib-q', 'lib-status'].forEach(id => $(id).addEventListener('input', loadEntries));
    $('lib-status').addEventListener('change', loadEntries);
    $('lib-tbody').addEventListener('change', e => {
        const box = e.target.closest('.lib-check');
        if (!box) return;
        if (box.checked) state.selected.add(box.dataset.key);
        else state.selected.delete(box.dataset.key);
    });
    $('lib-checkall').addEventListener('change', e => {
        const checked = e.target.checked;
        state.selected.clear();
        if (checked) state.entries.forEach(en => state.selected.add(`${en.ip}|${en.port || 0}`));
        renderTable();
    });

    loadLibraries();
    return { refresh: loadLibraries };
}
