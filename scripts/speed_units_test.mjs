import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('../web/js/speed-units.js', import.meta.url), 'utf8');
const moduleURL = 'data:text/javascript;base64,' + Buffer.from(source).toString('base64');
const { SPEED_KBS_PER_MBPS, mbpsToSpeedKBs, speedKBsToMbps, formatSpeedMbps } = await import(moduleURL);
const tasksSource = readFileSync(new URL('../web/js/tasks.js', import.meta.url), 'utf8');

assert.equal(SPEED_KBS_PER_MBPS, 122.0703125);
assert.equal(mbpsToSpeedKBs(1), 122.0703125);
assert.equal(speedKBsToMbps(1024), 8.388608);
assert.equal(formatSpeedMbps(1024), '8.388608');
assert.ok(Math.abs(speedKBsToMbps(mbpsToSpeedKBs(37.5)) - 37.5) < 1e-12);

for (const value of [0, '', null, undefined, -1, 'invalid', Number.NaN, Number.POSITIVE_INFINITY]) {
    assert.equal(mbpsToSpeedKBs(value), 0);
    assert.equal(speedKBsToMbps(value), 0);
    assert.equal(formatSpeedMbps(value), '');
}

assert.match(tasksSource, /formatSpeedMbps\(rule\.speedMin\)/);
assert.match(tasksSource, /speedMin: task\.speedEnabled \? mbpsToSpeedKBs\(num\('\.r-spd-min'\)\) : 0/);
assert.match(tasksSource, /class="r-spd-max"[^>]*>[\s\S]*?Mbps<\/label>/);

console.log('speed units: all assertions passed');