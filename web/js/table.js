// table.js —— 结果表格模块：渲染、排序、过滤、选择、国家配额

const COLUMNS = [
    { key: '_sel', label: '', sortable: false },
    { key: 'ip', label: 'IP 地址', sortable: true },
    { key: 'port', label: '端口', sortable: true, type: 'number' },
    { key: 'tcpLatencyMs', label: '延迟', sortable: true, type: 'number', render: r => latencyBadge(r.tcpLatencyMs) },
    { key: 'downloadSpeedKBs', label: '速度', sortable: true, type: 'number', render: r => speedText(r.downloadSpeedKBs) },
    { key: 'dataCenter', label: '数据中心', sortable: true },
    { key: 'emoji', label: '国旗', sortable: false },
    { key: 'country', label: '国家', sortable: true },
    { key: 'cityZh', label: '城市', sortable: true, render: r => r.cityZh || r.city || '' },
    { key: 'ipType', label: '出站', sortable: true },
    { key: 'asnOrg', label: 'ASN 组织', sortable: true },
];

function latencyBadge(ms) {
    const cls = ms <= 150 ? 'fast' : ms <= 400 ? 'mid' : 'slow';
    return `<span class="badge ${cls}">${ms} ms</span>`;
}

function speedText(kbs) {
    if (!kbs) return '<span class="badge none">未测</span>';
    const cls = kbs >= 1000 ? 'fast' : kbs >= 100 ? 'mid' : 'slow';
    const text = kbs >= 1024 ? `${(kbs / 1024).toFixed(1)} MB/s` : `${kbs.toFixed(0)} kB/s`;
    return `<span class="badge ${cls}">${text}</span>`;
}

function escapeHTML(s) {
    return String(s ?? '').replace(/[&<>"']/g, c =>
        ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

/** 结果表格：持有全部结果，支持逐条追加、排序、过滤、勾选、配额选择 */
export class ResultTable {
    constructor(containerEl) {
        this.container = containerEl;
        this.results = [];            // 全部结果（原始顺序 = 到达顺序）
        this.selectedKeys = new Set(); // 勾选集合 "ip|port"
        this.sortKey = 'tcpLatencyMs';
        this.sortAsc = true;
        this.filterText = '';
        this.quotaPicks = null;        // 国家配额选中的 key 集合（与勾选并集）
        this._renderScheduled = false; // 渲染节流：SSE 高频事件时合并重绘
        this._buildSkeleton();
    }

    static keyOf(r) { return `${r.ip}|${r.port}`; }

    _buildSkeleton() {
        const thead = COLUMNS.map(col => {
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
                        <tr class="empty-row"><td colspan="${COLUMNS.length}">暂无结果 —— 请先运行延迟测试</td></tr>
                    </tbody>
                </table>
            </div>`;

        this.tbody = this.container.querySelector('#result-tbody');

        this.container.querySelectorAll('thead th[data-key]').forEach(th => {
            th.addEventListener('click', () => {
                const key = th.dataset.key;
                const col = COLUMNS.find(c => c.key === key);
                if (!col?.sortable) return;
                if (this.sortKey === key) this.sortAsc = !this.sortAsc;
                else { this.sortKey = key; this.sortAsc = true; }
                this.render();
            });
        });

        this.container.querySelector('#sel-all').addEventListener('change', e => {
            const visible = this._visibleResults();
            if (e.target.checked) visible.forEach(r => this.selectedKeys.add(ResultTable.keyOf(r)));
            else visible.forEach(r => this.selectedKeys.delete(ResultTable.keyOf(r)));
            this.render();
        });
    }

    /** 清空所有结果（新任务开始时调用） */
    clear() {
        this.results = [];
        this.selectedKeys.clear();
        this.quotaPicks = null;
        this.render();
    }

    /** SSE result 事件：追加一条 */
    appendResult(result) {
        this.results.push(result);
        this.render();
    }

    /** SSE speed 事件：按 ip:port 回填速度 */
    updateSpeed(r) {
        const found = this.results.find(x => x.ip === r.ip && x.port === r.port);
        if (found) {
            found.downloadSpeedKBs = r.downloadSpeedKBs;
            this.render();
        }
    }

    setFilter(text) {
        this.filterText = (text || '').toLowerCase();
        this.render();
    }

    /** 返回国家统计 [{name, emoji, count}]，按数量降序 */
    getCountryStats() {
        const stats = new Map();
        for (const r of this.results) {
            const name = r.country || '未知';
            const cur = stats.get(name) || { name, emoji: r.emoji || '', count: 0 };
            cur.count++;
            stats.set(name, cur);
        }
        return [...stats.values()].sort((a, b) => b.count - a.count);
    }

    /**
     * 按国家配额选择：{ "日本": 5, "美国": 10 }。
     * 每国取当前排序下的前 N 个；quotas 为空对象/null 时清除配额选择。
     */
    applyCountryQuotas(quotas) {
        this.quotaPicks = new Set();
        if (quotas) {
            const sorted = this._sortedResults();
            const taken = {};
            for (const r of sorted) {
                const country = r.country || '未知';
                const quota = quotas[country];
                if (!quota) continue;
                taken[country] = (taken[country] || 0);
                if (taken[country] < quota) {
                    taken[country]++;
                    this.quotaPicks.add(ResultTable.keyOf(r));
                }
            }
        }
        this.render();
    }

    _visibleResults() {
        if (!this.filterText) return this._sortedResults();
        return this._sortedResults().filter(r =>
            [r.ip, r.port, r.country, r.cityZh, r.city, r.dataCenter, r.asnOrg, r.emoji]
                .some(v => String(v ?? '').toLowerCase().includes(this.filterText)));
    }

    _sortedResults() {
        const col = COLUMNS.find(c => c.key === this.sortKey);
        const arr = [...this.results];
        if (!col?.sortable) return arr;
        arr.sort((a, b) => {
            const va = a[this.sortKey], vb = b[this.sortKey];
            let cmp;
            if (col.type === 'number') cmp = (va || 0) - (vb || 0);
            else cmp = String(va ?? '').localeCompare(String(vb ?? ''), 'zh-CN');
            return this.sortAsc ? cmp : -cmp;
        });
        return arr;
    }

    /** 当前勾选（手动勾选 ∪ 配额选择）的结果 */
    getSelectedResults() {
        const union = new Set([...this.selectedKeys, ...(this.quotaPicks || [])]);
        return this._sortedResults().filter(r => union.has(ResultTable.keyOf(r)));
    }

    getAllResults() { return this._sortedResults(); }

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
            this.tbody.innerHTML = `<tr class="empty-row"><td colspan="${COLUMNS.length}">${this.results.length ? '没有匹配过滤条件的结果' : '暂无结果 —— 请先运行延迟测试'}</td></tr>`;
            return;
        }

        const union = new Set([...this.selectedKeys, ...(this.quotaPicks || [])]);
        this.tbody.innerHTML = visible.map(r => {
            const key = ResultTable.keyOf(r);
            const checked = union.has(key) ? 'checked' : '';
            const cells = COLUMNS.map(col => {
                if (col.key === '_sel') return `<td><input type="checkbox" data-key="${escapeHTML(key)}" ${checked}></td>`;
                const val = col.render ? col.render(r) : escapeHTML(r[col.key]);
                const cls = col.type === 'number' ? 'num' : (col.key === 'ip' ? 'mono' : '');
                return `<td class="${cls}">${val}</td>`;
            }).join('');
            return `<tr class="${checked ? 'selected' : ''}">${cells}</tr>`;
        }).join('');

        this.tbody.querySelectorAll('input[type="checkbox"]').forEach(cb => {
            cb.addEventListener('change', () => {
                if (cb.checked) this.selectedKeys.add(cb.dataset.key);
                else this.selectedKeys.delete(cb.dataset.key);
                this.render();
            });
        });
    }
}

/** 全部可导出的 CSV 列（与 CLI 版 27 列对齐） */
export const CSV_COLUMNS = [
    { key: 'ip', label: 'IP地址' },
    { key: 'port', label: '端口号' },
    { key: 'enableTLS', label: 'TLS' },
    { key: 'dataCenter', label: '数据中心' },
    { key: 'locCode', label: 'IP位置' },
    { key: 'region', label: '地区' },
    { key: 'city', label: '城市' },
    { key: 'regionZh', label: '地区(中文)' },
    { key: 'country', label: '出站IP位置' },
    { key: 'cityZh', label: '城市(中文)' },
    { key: 'emoji', label: '国旗' },
    { key: 'tcpLatencyMs', label: '网络延迟' },
    { key: 'downloadSpeedKBs', label: '下载速度' },
    { key: 'outboundIP', label: '出站IP' },
    { key: 'ipType', label: '出站IP类型' },
    { key: 'ipsType', label: 'IPS类型' },
    { key: 'asn', label: 'ASN号码' },
    { key: 'asnOrg', label: 'ASN组织' },
    { key: 'visitScheme', label: '访问协议' },
    { key: 'tlsVersion', label: 'TLS版本' },
    { key: 'sni', label: 'SNI' },
    { key: 'httpVersion', label: 'HTTP版本' },
    { key: 'warp', label: 'WARP' },
    { key: 'gateway', label: 'Gateway' },
    { key: 'rbi', label: 'RBI' },
    { key: 'kex', label: '密钥交换' },
    { key: 'timestamp', label: '时间戳' },
];
