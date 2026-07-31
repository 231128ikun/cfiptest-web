// table.js —— 结果表格模块：渲染、排序、过滤、选择、分组配额

import { TABLE_COLUMNS, columnByKey, escapeHTML } from './columns.js';

/** 可用于配额分组的维度（界面上的维度下拉直接由此派生） */
export const GROUP_DIMENSIONS = [
    { key: 'country', label: '国家' },
    { key: 'asnOrg', label: 'ASN 组织' },
    { key: 'dataCenter', label: '数据中心' },
    { key: 'ipType', label: '出站类型' },
];

// 虚拟滚动参数。行数不多时全量渲染最简单也最快，超过阈值才切窗口渲染——
// 官方段每 /24 取 1 个就是 5956 个目标，全测出来一次 innerHTML 拼 6000 行
// 会让每次勾选/排序都卡住半秒。
const VIRTUAL_THRESHOLD = 500;
const VIRTUAL_OVERSCAN = 10;   // 视口上下各多渲染几行，滚动时不露白
const ROW_H_FALLBACK = 33;     // 首帧还没量到真实行高时的估值

/** 结果表格：持有全部结果，支持逐条追加、排序、过滤、勾选、配额选择 */
export class ResultTable {
    constructor(containerEl, columns = TABLE_COLUMNS) {
        this.container = containerEl;
        this.columns = columns;
        this.results = [];             // 全部结果（原始顺序 = 到达顺序）
        this.selectedKeys = new Set(); // 唯一的勾选集合 "ip|port"
        this.sortKey = 'tcpLatencyMs';
        this.sortAsc = true;
        this.filterText = '';
        this.filters = {};
        this.displayQuota = null;
        this._renderScheduled = false; // 渲染节流：SSE 高频事件时合并重绘
        this._sortedCache = null;      // _sortedResults 的缓存
        this._rowH = 0;                // 实测行高（虚拟滚动用）
        this._buildSkeleton();
    }

    static keyOf(r) { return `${r.ip}|${r.port}`; }

    /** 结果集/排序变化后调用：让派生缓存失效。 */
    _invalidate() { this._sortedCache = null; }

    _buildSkeleton() {
        const thead = this.columns.map(col => {
            if (col.key === '_sel') {
                return `<th class="no-sort"><input type="checkbox" id="sel-all" title="全选"></th>`;
            }
            return `<th data-key="${col.key}" class="${col.sortable ? '' : 'no-sort'}">${col.label}<span class="arrow"></span></th>`;
        }).join('');

        this.container.innerHTML = `
            <div class="table-wrap">
                <table class="results">
                    <thead><tr>${thead}</tr></thead>
                    <tbody id="result-tbody">
                        <tr class="empty-row"><td colspan="${this.columns.length}">暂无结果 —— 请先运行延迟测试</td></tr>
                    </tbody>
                </table>
            </div>`;

        this.tbody = this.container.querySelector('#result-tbody');
        this.wrap = this.container.querySelector('.table-wrap');

        // 虚拟滚动：滚动时重算窗口。行数少于阈值时 _renderNow 直接全量渲染，
        // 这个监听器实际不做事（_renderNow 会算出整个区间）。
        this.wrap?.addEventListener('scroll', () => {
            if (this.results.length >= VIRTUAL_THRESHOLD) this.render();
        });

        this.container.querySelectorAll('thead th[data-key]').forEach(th => {
            th.addEventListener('click', () => {
                const key = th.dataset.key;
                const col = columnByKey(key);
                if (!col?.sortable) return;
                // 点当前列 = 反向；点别的列 = 换列并复位为升序
                this.setSort(key, this.sortKey === key ? !this.sortAsc : true);
            });
        });

        this.container.querySelector('#sel-all').addEventListener('change', e => {
            const visible = this._visibleResults();
            if (e.target.checked) visible.forEach(r => this.selectedKeys.add(ResultTable.keyOf(r)));
            else visible.forEach(r => this.selectedKeys.delete(ResultTable.keyOf(r)));
            this.render();
        });

        // 勾选用事件委托：只绑一次，勾选时不重建 DOM。
        // 改造前每次勾选都 tbody.innerHTML 全量重建并重绑所有监听——
        // 等于「点一下勾选框就把你刚点的那一行销毁重建」，行数一多就明显卡顿。
        this.tbody.addEventListener('change', e => {
            const cb = e.target;
            if (cb.type !== 'checkbox' || !cb.dataset.key) return;
            if (cb.checked) this.selectedKeys.add(cb.dataset.key);
            else this.selectedKeys.delete(cb.dataset.key);
            // 只切换本行样式，不重绘整表
            cb.closest('tr')?.classList.toggle('selected', cb.checked);
            this._syncSelectAll();
            this.container.dispatchEvent(new CustomEvent('selectionchange', { bubbles: true }));
        });
    }

    /**
     * 设置排序。表头点击与排序下拉都走这里，两条入口共用一份状态，
     * 之后派发 sortchange 让下拉回填——否则点表头排序后下拉还显示旧列，
     * 两个控件各说各话。
     */
    setSort(key, asc = true) {
        const col = columnByKey(key);
        if (!col?.sortable) return;
        this.sortKey = key;
        this.sortAsc = !!asc;
        this._invalidate();
        this.render();
        this.container.dispatchEvent(new CustomEvent('sortchange', {
            bubbles: true,
            detail: { key: this.sortKey, asc: this.sortAsc },
        }));
    }

    /** 全选框状态跟随可见行：全选/部分选/未选。 */
    _syncSelectAll() {
        const box = this.container.querySelector('#sel-all');
        if (!box) return;
        const visible = this._visibleResults();
        const selected = visible.filter(r => this.selectedKeys.has(ResultTable.keyOf(r))).length;
        box.checked = visible.length > 0 && selected === visible.length;
        box.indeterminate = selected > 0 && selected < visible.length;
    }

    /** 清空所有结果（新任务开始时调用） */
    clear() {
        this.results = [];
        this.selectedKeys.clear();
        this.displayQuota = null;
        this._invalidate();
        this.render();
    }

    /** SSE result 事件：追加一条 */
    appendResult(result) {
        this.results.push(result);
        this._invalidate();
        this.render();
    }

    /** SSE speed 事件：按 ip:port 回填速度 */
    updateSpeed(r) {
        const found = this.results.find(x => x.ip === r.ip && x.port === r.port);
        if (found) {
            found.downloadSpeedKBs = r.downloadSpeedKBs;
            this._invalidate();
            this.render();
        }
    }

    setFilter(text) {
        this.filterText = (text || '').toLowerCase();
        this.render();
    }

    /** 设置结构化筛选：country / maxLatency / minSpeed。空值表示不限制。 */
    setFilters(filters = {}) {
        this.filters = { ...filters };
        this.render();
    }

    /** 动态设置展示列；勾选列始终保留。 */
    setColumns(keys) {
        const unique = [...new Set(Array.isArray(keys) ? keys : [])];
        const wanted = ['_sel', ...unique.filter(k => k !== '_sel')];
        const columns = wanted.map(columnByKey).filter(Boolean);
        if (columns.length < 2) return;
        this.columns = columns;
        this._buildSkeleton();
        this.render();
    }

    /** 按当前筛选和排序，把每组前 N 个作为当前展示集合。 */
    applyGroupDisplayQuotas(groupBy, quotas) {
        const normalized = Object.fromEntries(Object.entries(quotas || {})
            .map(([group, value]) => [group, Math.max(0, Number(value) || 0)])
            .filter(([, value]) => value > 0));
        this.displayQuota = Object.keys(normalized).length ? { groupBy, quotas: normalized } : null;
        const shown = this._filteredResults(true).length;
        this.render();
        return shown;
    }

    clearDisplayQuotas() {
        this.displayQuota = null;
        this.render();
    }

    _applyDisplayQuota(rows) {
        if (!this.displayQuota) return rows;
        const { groupBy, quotas } = this.displayQuota;
        const taken = new Map();
        return rows.filter(r => {
            const group = r[groupBy] || '未知';
            const quota = quotas[group] || 0;
            const used = taken.get(group) || 0;
            if (quota <= 0 || used >= quota) return false;
            taken.set(group, used + 1);
            return true;
        });
    }

    /**
     * 返回分组统计 [{name, emoji, count}]，按数量降序。
     * emoji 只对国家维度有意义——按 ASN 分组时同组内各行国旗并不相同，
     * 取第一条的国旗会让人以为「这个 ASN 属于这个国家」。
     */
    getGroupStats(groupBy = 'country', { filtered = false } = {}) {
        const stats = new Map();
        const rows = filtered ? this._filteredResults(false) : this.results;
        for (const r of rows) {
            const name = r[groupBy] || '未知';
            const cur = stats.get(name)
                || { name, emoji: groupBy === 'country' ? (r.emoji || '') : '', count: 0 };
            cur.count++;
            stats.set(name, cur);
        }
        return [...stats.values()].sort((a, b) => b.count - a.count);
    }

    /** 兼容旧调用名。 */
    getCountryStats() { return this.getGroupStats('country'); }

    /**
     * 按分组配额选择：每组取前 N 个。
     *
     * 语义（本次明确）：在【当前筛选后】的结果集上，按【当前排序】分组取前 N。
     * 改造前用的是 _sortedResults()，忽略了筛选——用户先筛日本再配额，
     * 配额却从全量里挑，是个静默的 quirk。
     *
     * 配额只是「批量写入 selectedKeys」的一种手段，不再是独立的第二个集合：
     * 改造前 getSelectedResults() 取 selectedKeys ∪ quotaPicks，导致
     * 取消勾选某个配额行时它仍留在 quotaPicks 里，界面上取消不掉。
     */
    applyGroupQuota(groupBy, n, { replace = true } = {}) {
        if (replace) this.selectedKeys.clear();
        const limit = Number(n);
        if (!Number.isInteger(limit) || limit <= 0) { this.render(); return 0; }

        const taken = new Map();
        let added = 0;
        for (const r of this._visibleResults()) {
            const group = r[groupBy] || '未知';
            const used = taken.get(group) || 0;
            if (used >= limit) continue;
            taken.set(group, used + 1);
            this.selectedKeys.add(ResultTable.keyOf(r));
            added++;
        }
        this.render();
        return added;
    }

    /**
     * 按「每组各自的配额」选择：applyGroupQuotas('country', { 日本: 5, 美国: 10 })。
     * 与 applyGroupQuota（统一 N）的区别只在配额来源，语义完全一致：
     * 在当前筛选后的集合上、按当前排序、每组取前 N。同样只写 selectedKeys。
     */
    applyGroupQuotas(groupBy, quotas) {
        this.selectedKeys.clear();
        if (quotas) {
            const taken = new Map();
            for (const r of this._visibleResults()) {
                const group = r[groupBy] || '未知';
                const quota = quotas[group];
                if (!quota) continue;
                const used = taken.get(group) || 0;
                if (used >= quota) continue;
                taken.set(group, used + 1);
                this.selectedKeys.add(ResultTable.keyOf(r));
            }
        }
        this.render();
        return this.selectedKeys.size;
    }

    /** 兼容旧调用名（国家维度）。 */
    applyCountryQuotas(quotas) { return this.applyGroupQuotas('country', quotas); }

    /** 清除全部勾选。 */
    clearSelection() {
        this.selectedKeys.clear();
        this.render();
    }

    _filteredResults(includeDisplayLimit = true) {
        let rows = this._sortedResults();
        if (this.filterText) {
            rows = rows.filter(r =>
                [r.ip, r.port, r.country, r.cityZh, r.city, r.dataCenter, r.asnOrg, r.emoji]
                    .some(v => String(v ?? '').toLowerCase().includes(this.filterText)));
        }
        const { country, maxLatency, minSpeed } = this.filters;
        if (country) rows = rows.filter(r => r.country === country || r.locCode === country);
        if (Number(maxLatency) > 0) rows = rows.filter(r => Number(r.tcpLatencyMs) <= Number(maxLatency));
        if (Number(minSpeed) > 0) rows = rows.filter(r => Number(r.downloadSpeedKBs) >= Number(minSpeed));
        if (includeDisplayLimit) rows = this._applyDisplayQuota(rows);
        return rows;
    }

    _visibleResults() { return this._filteredResults(true); }

    /** 排序结果（带缓存：改造前每次 render 要重算 3–4 次）。 */
    _sortedResults() {
        if (this._sortedCache) return this._sortedCache;
        const col = columnByKey(this.sortKey);
        const arr = [...this.results];
        if (!col?.sortable) { this._sortedCache = arr; return arr; }
        arr.sort((a, b) => {
            const va = a[this.sortKey], vb = b[this.sortKey];
            let cmp;
            if (col.type === 'number') cmp = (va || 0) - (vb || 0);
            else cmp = String(va ?? '').localeCompare(String(vb ?? ''), 'zh-CN');
            return this.sortAsc ? cmp : -cmp;
        });
        this._sortedCache = arr;
        return arr;
    }

    /** 当前勾选的结果（唯一来源 selectedKeys，按当前排序返回）。 */
    getSelectedResults() {
        return this._sortedResults().filter(r => this.selectedKeys.has(ResultTable.keyOf(r)));
    }

    /** 当前筛选可见的全部结果。 */
    getAllResults() { return this._visibleResults(); }

    /** 全部结果，忽略筛选与展示数量限制，但保留当前排序。 */
    getResults() { return this._sortedResults(); }

    /** 请求重绘：用 rAF 合并高频调用（SSE 事件风暴时避免 O(n²) 卡顿） */
    render() {
        if (this._renderScheduled) return;
        this._renderScheduled = true;
        requestAnimationFrame(() => {
            this._renderScheduled = false;
            this._renderNow();
        });
    }

    _renderNow() {
        const visible = this._visibleResults();

        // 更新表头箭头
        this.container.querySelectorAll('thead th[data-key]').forEach(th => {
            const arrow = th.querySelector('.arrow');
            if (th.dataset.key === this.sortKey) arrow.textContent = this.sortAsc ? ' ▲' : ' ▼';
            else arrow.textContent = '';
        });

        if (!visible.length) {
            this.tbody.innerHTML = `<tr class="empty-row"><td colspan="${this.columns.length}">${this.results.length ? '没有匹配过滤条件的结果' : '暂无结果 —— 请先运行延迟测试'}</td></tr>`;
            this._syncSelectAll();
            return;
        }

        const { start, end, padTop, padBottom } = this._window(visible.length);
        const rows = visible.slice(start, end).map(r => this._rowHTML(r)).join('');

        // 上下用一对撑高的空行占位，让滚动条长度与总行数一致。
        // 这样滚动位置、拖动手感都和全量渲染时一样，只是 DOM 里只有一屏行。
        const span = this.columns.length;
        this.tbody.innerHTML =
            (padTop ? `<tr class="pad" style="height:${padTop}px"><td colspan="${span}"></td></tr>` : '')
            + rows
            + (padBottom ? `<tr class="pad" style="height:${padBottom}px"><td colspan="${span}"></td></tr>` : '');

        // 量一次真实行高，之后的窗口计算就准了（首帧用估值，误差只影响一帧）
        if (!this._rowH && this.wrap) {
            const tr = this.tbody.querySelector('tr:not(.pad)');
            if (tr?.offsetHeight) this._rowH = tr.offsetHeight;
        }

        this._syncSelectAll();
    }

    /** 计算当前该渲染哪一段行。行数少于阈值时返回整个区间。 */
    _window(total) {
        if (total < VIRTUAL_THRESHOLD || !this.wrap) {
            return { start: 0, end: total, padTop: 0, padBottom: 0 };
        }
        const rowH = this._rowH || ROW_H_FALLBACK;
        const viewH = this.wrap.clientHeight || 420;
        const first = Math.floor((this.wrap.scrollTop || 0) / rowH);
        const count = Math.ceil(viewH / rowH) + VIRTUAL_OVERSCAN * 2;
        const start = Math.max(0, first - VIRTUAL_OVERSCAN);
        const end = Math.min(total, start + count);
        return { start, end, padTop: start * rowH, padBottom: (total - end) * rowH };
    }

    _rowHTML(r) {
        const key = ResultTable.keyOf(r);
        const checked = this.selectedKeys.has(key) ? 'checked' : '';
        const cells = this.columns.map(col => {
            if (col.key === '_sel') return `<td><input type="checkbox" data-key="${escapeHTML(key)}" ${checked}></td>`;
            const val = col.render ? col.render(r) : escapeHTML(r[col.key]);
            const cls = col.type === 'number' ? 'num' : (col.key === 'ip' ? 'mono' : '');
            return `<td class="${cls}">${val}</td>`;
        }).join('');
        return `<tr class="${checked ? 'selected' : ''}">${cells}</tr>`;
    }
}

// CSV 列定义已移入 columns.js（与表格列合并为单一注册表）。
// 这里重新导出，避免调用方需要同时 import 两个模块。
export { CSV_COLUMNS } from './columns.js';
