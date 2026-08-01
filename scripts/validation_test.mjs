import assert from 'node:assert/strict';
import { boundedNumber } from '../web/js/validation.js';

let pass = 0;
const check = (name, fn) => {
    try { fn(); pass++; console.log(`  ok  ${name}`); }
    catch (e) { console.log(`  FAIL ${name}\n       ${e.message}`); process.exitCode = 1; }
};

check('严格拒绝带尾随字符的数值', () => {
    assert.deepEqual(
        boundedNumber('12abc', { min: 1, max: 100, integer: true }),
        { value: 1, changed: true, empty: false },
    );
});
check('整数会截断并限制在范围内', () => {
    assert.deepEqual(
        boundedNumber('120.9', { min: 1, max: 100, integer: true }),
        { value: 100, changed: true, empty: false },
    );
});
check('有限小数保持精度', () => {
    assert.deepEqual(
        boundedNumber('12.5', { min: 0, max: 20 }),
        { value: 12.5, changed: false, empty: false },
    );
});
check('空值保留为空值语义', () => {
    assert.deepEqual(
        boundedNumber('', { min: 1, max: 100, integer: true, emptyValue: 0 }),
        { value: 0, changed: false, empty: true },
    );
});
check('Infinity 与 NaN 回落后仍受最小值约束', () => {
    assert.equal(boundedNumber('Infinity', { min: 200, max: 10000, integer: true }).value, 200);
    assert.equal(boundedNumber('NaN', { min: 1, max: 30, integer: true }).value, 1);
});
check('负数在允许零值的字段中归零', () => {
    assert.deepEqual(
        boundedNumber('-10', { min: 0 }),
        { value: 0, changed: true, empty: false },
    );
});

console.log(`\n通过 ${pass} 项`);
