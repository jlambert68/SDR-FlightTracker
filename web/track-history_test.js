const test=require('node:test');
const assert=require('node:assert/strict');

require('./track-history.js');

const {mergeHistoryPoints,refreshBefore,splitAtGaps,unresolvedSignalSegments}=globalThis.FlightTrackHistory;
const start=Date.parse('2026-10-08T19:00:00Z');
const point=(seconds,latitude=59,longitude=18)=>({timestamp:new Date(start+seconds*1000).toISOString(),latitude,longitude,altitudeFeet:9000});
const signal=seconds=>({timestamp:new Date(start+seconds*1000).toISOString(),milliseconds:start+seconds*1000,id:`signal-${seconds}`});

test('accumulates refreshed history and replaces duplicate timestamps',()=>{
 const existing=mergeHistoryPoints([], [point(0),point(10)],new Date(start+20_000).toISOString());
 const refreshed=mergeHistoryPoints(existing,[point(10,59.1),point(20),point(40)],new Date(start+30_000).toISOString());
 assert.deepEqual(refreshed.map(item=>item.milliseconds),[start,start+10_000,start+20_000]);
 assert.equal(refreshed[1].latitude,59.1);
});

test('splits internet history instead of drawing across a data gap',()=>{
 const history=mergeHistoryPoints([],[point(0),point(10),point(150),point(160)],new Date(start+200_000).toISOString());
 const segments=splitAtGaps(history,120_000);
 assert.deepEqual(segments.map(segment=>segment.map(item=>item.milliseconds)),[[start,start+10_000],[start+150_000,start+160_000]]);
});

test('keeps only signal intervals not yet covered by internet history',()=>{
 const signals=[signal(0),signal(10),signal(20)];
 const partialHistory=mergeHistoryPoints([], [point(-10),point(0),point(5),point(15)],new Date(start+16_000).toISOString());
 const partial=unresolvedSignalSegments(signals,partialHistory,120_000);
 assert.deepEqual(partial.map(segment=>segment.map(item=>item.id)),[['signal-10','signal-20']]);
 const completeHistory=mergeHistoryPoints(partialHistory,[point(25)],new Date(start+30_000).toISOString());
 assert.deepEqual(unresolvedSignalSegments(signals,completeHistory,120_000),[]);
});

test('keeps a signal fallback when internet history stops short',()=>{
 const signals=[signal(0),signal(10)];
 const history=mergeHistoryPoints([], [point(-20),point(-5)],new Date(start).toISOString());
 const fallback=unresolvedSignalSegments(signals,history,120_000);
 assert.equal(fallback.length,1);
 assert.equal(fallback[0][0].milliseconds,start-5_000);
 assert.deepEqual(fallback[0].slice(1).map(item=>item.id),['signal-0','signal-10']);
});

test('advances live and replay history requests after the latest signal',()=>{
 const latestSignal=signal(0),sixteenSeconds=16_000,fourDays=4*24*60*60*1000;
 assert.equal(refreshBefore(latestSignal,start+100,start+sixteenSeconds,600_000),new Date(start+sixteenSeconds).toISOString());
 assert.equal(refreshBefore(latestSignal,start+fourDays,start+fourDays+sixteenSeconds,600_000),new Date(start+sixteenSeconds).toISOString());
});
