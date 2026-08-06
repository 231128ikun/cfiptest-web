// quota-rules.js —— 自定义展示规则：供测速工作台和 IP 库页共用。
//
// 从 app.js 中抽取 addQuotaRule / readQuotaRules / clearQuotaEditors / refreshQuotaEditors，
// 使两个页面都能使用同一套「分组字段 + 取值顺序」规则编辑器，无需维护两份代码。

import { GROUP_COLUMNS } from './columns.js';
import { createMultiSelect } from './multiselect.js';

let seq = 0;
const editors = new Map(); // id → { row, conditions: [{ line, picker }] }

/** 获取某维度的可选值（含计数），供 picker 使用。 */
function quotaItems(dimension, table) {
    return table.getGroupStats(dimension, { filtered: true }).map(item => ({
        value: String(item.name),
        label: item.emoji ? `${item.emoji} ${item.name}` : String(item.name),
        count: item.count,
    }));
}

/** 在指定容器中添加一条规则编辑器。seed 为恢复/初始化用的预设值。 */
function addQuotaRule(container, table, seed = {}) {
    const id = `quota-rule-${++seq}`;
    const row = document.createElement('div');
    row.className = 'quota-rule';
    row.dataset.id = id;
    row.innerHTML = `<div class="quota-rule-head"><strong>规则 ${seq}</strong><span class="hint">每个值取前</span><input class="quota-rule-limit" type="number" min="0" value="${Number(seed.limit) || ''}" placeholder="无限制"><span class="hint">个</span><button class="small quota-rule-add-condition" type="button">添加限制字段</button><button class="small quota-rule-remove" type="button">删除规则</button></div><div class="quota-conditions"></div>`;
    container.appendChild(row);
    const conditions = [];

    function addCondition(condition = {}) {
        const line = document.createElement('div');
        line.className = 'quota-condition';
        line.innerHTML = `<span class="quota-condition-role"></span><select>${GROUP_COLUMNS.map(item => `<option value="${item.key}">${item.label}</option>`).join('')}</select><span class="quota-rule-picker"></span><button class="small quota-condition-remove" type="button">删除</button>`;
        row.querySelector('.quota-conditions').appendChild(line);
        const dimension = line.querySelector('select');
        dimension.value = condition.field || condition.dimension || seed.dimension || 'country';
        const picker = createMultiSelect(line.querySelector('.quota-rule-picker'), { placeholder: '选择一个或多个值' });
        const refill = values => {
            const selected = (values || picker.getSelectedInOrder()).map(String);
            const items = quotaItems(dimension.value, table);
            const known = new Set(items.map(item => item.value));
            selected.filter(value => !known.has(value)).forEach(value => items.push({ value, label: value, count: 0 }));
            picker.setItems(items); picker.setSelected(selected);
        };
        refill(condition.values || seed.values || []);
        dimension.addEventListener('change', () => refill([]));
        line.querySelector('.quota-condition-remove').addEventListener('click', () => {
            picker.destroy();
            const idx = conditions.findIndex(item => item.line === line);
            if (idx >= 0) conditions.splice(idx, 1);
            line.remove();
            updateRoles();
        });
        conditions.push({ line, picker });
        updateRoles();
    }

    function updateRoles() {
        conditions.forEach((item, index) => {
            item.line.querySelector('.quota-condition-role').textContent = index === 0 ? '分组字段' : '限制字段';
            item.line.querySelector('.quota-condition-remove').disabled = index === 0 && conditions.length === 1;
        });
    }

    const initial = Array.isArray(seed.conditions) && seed.conditions.length ? seed.conditions : [{ field: seed.dimension || 'country', values: seed.values || [] }];
    initial.forEach(addCondition);
    row.querySelector('.quota-rule-add-condition').addEventListener('click', () => addCondition());
    row.querySelector('.quota-rule-remove').addEventListener('click', () => {
        conditions.forEach(item => item.picker.destroy());
        editors.delete(id);
        row.remove();
    });
    editors.set(id, { row, conditions });
}

/** 从所有规则编辑器中读取规则数组。 */
function readQuotaRules() {
    return [...editors.values()].map(({ row, conditions }) => ({
        conditions: conditions.map(({ line, picker }) => ({
            field: line.querySelector('select').value,
            values: picker.getSelectedInOrder(),
        })).filter(condition => condition.values.length),
        limit: Number(row.querySelector('.quota-rule-limit').value) || 0,
    })).filter(rule => rule.conditions.length);
}

/** 清空所有规则编辑器（销毁 picker 监听器）。 */
function clearQuotaEditors() {
    for (const { conditions } of editors.values()) conditions.forEach(item => item.picker.destroy());
    editors.clear();
}

/** 刷新所有 picker 的候选项（维度切换后调用）。 */
function refreshQuotaEditors(table) {
    for (const { conditions } of editors.values()) {
        conditions.forEach(({ line, picker }) => {
            const selected = picker.getSelectedInOrder();
            const items = quotaItems(line.querySelector('select').value, table);
            const known = new Set(items.map(item => item.value));
            selected.filter(value => !known.has(value)).forEach(value => items.push({ value, label: value, count: 0 }));
            picker.setItems(items); picker.setSelected(selected);
        });
    }
}

export { addQuotaRule, readQuotaRules, clearQuotaEditors, refreshQuotaEditors };
