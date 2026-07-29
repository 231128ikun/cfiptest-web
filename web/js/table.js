// table.js —— 结果表格模块：渲染、排序、过滤、选择、分组配额

import { TABLE_COLUMNS, columnByKey, escapeHTML } from './columns.js';

/** 可用于配额分组的维度（B3 会在界面暴露；此处先支持泛化调用） */
export const GROUP_DIMENSIONS = [
    { key: 'country', label: '国家' },
    { key: 'asnOrg', label: 'ASN 组织' },
    { key: 'dataCenter', label: '数据中心' },
    { key: 'ipType', label: '出站类型' },
];

/** 结果表格：持有全部结果，支持逐条追加、排序、过滤、勾选、配额选择 */
export class ResultTable {
    constructor(containerEl) {
        this.container = containerEl;
        this.results = [];             // 全部结果（原始顺序 = 到达顺序）
        this.selectedKeys = new Set(); // 唯一的勾选集合 "ip|port"
        this.sortKey = 'tcpLatencyMs';
        this.sortAsc = true;
        this.filterText = '';
        this._renderScheduled = false; // 渲染节流：SSE 高频事件时合并重绘
        this._sortedCache = null;      // _sortedResults 的缓存
        this._buildSkeleton();
    }

    static keyOf(r) { return `${r.ip}|${r.port}`; }

    /** 结果集/排序变化后调用：让派生缓存失效。 */
    _invalidate() { this._sortedCache = null; }

    _buildSkeleton() {
        const thead = TABLE_COLUMNS.map(col => {
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
                        <tr class="empty-row"><td colspan="${TABLE_COLUMNS.length}">暂无结果 —— 请先运行延迟测试</td></tr>
                    </tbody>
                </table>
            </div>`;

        this.tbody = this.container.querySelector('#result-tbody');

        this.container.querySelectorAll('thead th[data-key]').forEach(th => {
            th.addEventListener('click', () => {
                const key = th.dataset.key;
                const col = columnByKey(key);
                if (!col?.sortable) return;
                if (this.sortKey === key) this.sortAsc = !this.sortAsc;
                else { this.sortKey = key; this.sortAsc = true; }
                this._invalidate();
                this.render();
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

    /** 返回分组统计 [{name, emoji, count}]，按数量降序。 */
    getGroupStats(groupBy = 'country') {
        const stats = new Map();
        for (const r of this.results) {
            const name = r[groupBy] || '未知';
            const cur = stats.get(name) || { name, emoji: r.emoji || '', count: 0 };
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
     * 按国家配额选择：{ "日本": 5, "美国": 10 }。
     * 保留此方法供现有界面调用；同样只写 selectedKeys。
     */
    applyCountryQuotas(quotas) {
        this.selectedKeys.clear();
        if (quotas) {
            const taken = {};
            for (const r of this._visibleResults()) {
                const country = r.country || '未知';
                const quota = quotas[country];
                if (!quota) continue;
                taken[country] = taken[country] || 0;
                if (taken[country] < quota) {
                    taken[country]++;
                    this.selectedKeys.add(ResultTable.keyOf(r));
                }
            }
        }
        this.render();
    }

    /** 清除全部勾选。 */
    clearSelection() {
        this.selectedKeys.clear();
        this.render();
    }

    _visibleResults() {
        if (!this.filterText) return this._sortedResults();
        return this._sortedResults().filter(r =>
            [r.ip, r.port, r.country, r.cityZh, r.city, r.dataCenter, r.asnOrg, r.emoji]
                .some(v => String(v ?? '').toLowerCase().includes(this.filterText)));
    }

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
            this.tbody.innerHTML = `<tr class="empty-row"><td colspan="${TABLE_COLUMNS.length}">${this.results.length ? '没有匹配过滤条件的结果' : '暂无结果 —— 请先运行延迟测试'}</td></tr>`;
            this._syncSelectAll();
            return;
        }

        this.tbody.innerHTML = visible.map(r => {
            const key = ResultTable.keyOf(r);
            const checked = this.selectedKeys.has(key) ? 'checked' : '';
            const cells = TABLE_COLUMNS.map(col => {
                if (col.key === '_sel') return `<td><input type="checkbox" data-key="${escapeHTML(key)}" ${checked}></td>`;
                const val = col.render ? col.render(r) : escapeHTML(r[col.key]);
                const cls = col.type === 'number' ? 'num' : (col.key === 'ip' ? 'mono' : '');
                return `<td class="${cls}">${val}</td>`;
            }).join('');
            return `<tr class="${checked ? 'selected' : ''}">${cells}</tr>`;
        }).join('');

        this._syncSelectAll();
    }
}

// CSV 列定义已移入 columns.js（与表格列合并为单一注册表）。
// 这里重新导出，避免调用方需要同时 import 两个模块。
export { CSV_COLUMNS } from './columns.js';
