// runs.js —— 运行记录页：每次维护运行的摘要列表（最新在前），可展开查看明细
import { escapeHTML } from './columns.js';
import * as api from './api.js';

const $ = id => document.getElementById(id);
const STATUS_LABEL = { completed: '成功', stopped: '已停止', error: '出错' };
const fmtTime = ts => {
    if (!ts) return '—';
    const d = new Date(ts);
    if (Number.isNaN(d.getTime())) return '—';
    return d.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
};

export function initRuns({ toast }) {
    async function load() {
        try {
            const data = await api.fetchRuns(200);
            render(data.runs || []);
        } catch (error) {
            toast(`加载运行记录失败：${error.message}`);
        }
    }

    function render(runs) {
        const wrap = $('runs-list');
        if (!runs.length) {
            wrap.innerHTML = '<div class="task-detail-empty">暂无运行记录：在「自动维护」页执行一次任务后，这里会展示结果。</div>';
            return;
        }
        wrap.innerHTML = runs.map((r, i) => {
            const badge = r.status === 'completed' ? (r.shortages?.length ? 'shortage' : 'ok') : (r.status === 'error' ? 'error' : 'shortage');
            const groups = (r.groups || []).map(g => `
                <div>${escapeHTML(g.name || '')}：${g.filled ?? 0}${g.target ? ` / ${g.target}` : ' / 不限'}，缺口 ${g.shortage ?? 0}，延迟失败 ${g.failed ?? 0}，测速失败(保留) ${g.speedFailed ?? 0}，回写 ${g.updated ?? 0}</div>`).join('');
            const shortages = (r.shortages || []).map(s => `<div>⚠ ${escapeHTML(s)}</div>`).join('');
            return `<div class="run-card ${i === 0 ? 'open' : ''}">
                <div class="run-card-head">
                    <span class="run-card-name">${escapeHTML(r.name || '未命名任务')}</span>
                    <span class="task-badge ${badge}">${STATUS_LABEL[r.status] || escapeHTML(r.status)}</span>
                    <span class="run-card-time">${fmtTime(r.startedAt)} · 耗时 ${Math.round((r.durationMs ?? 0) / 1000)}s</span>
                    <button type="button" class="small run-toggle">展开/收起</button>
                </div>
                <div class="run-meta">输出 ${r.totalLines ?? 0} 行 · 移除失效 ${r.removedDead ?? 0} · 输入新增 ${r.inputAdded ?? 0}</div>
                ${r.error ? `<div class="auto-shortage">× ${escapeHTML(r.error)}</div>` : ''}
                <div class="run-card-detail">
                    ${r.outputPath ? `<div>输出文件：${escapeHTML(r.outputPath)}</div>` : ''}
                    ${groups}
                    ${shortages}
                </div>
            </div>`;
        }).join('');
    }

    $('runs-list').addEventListener('click', e => {
        const btn = e.target.closest('.run-toggle');
        if (btn) btn.closest('.run-card').classList.toggle('open');
    });
    $('runs-refresh').addEventListener('click', load);
    load();
    return { refresh: load };
}
