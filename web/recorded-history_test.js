const test=require('node:test');
const assert=require('node:assert/strict');

require('./recorded-history.js');

const {clampCursor,snapshotAt}=globalThis.RecordedHistory;
const start=Date.parse('2026-10-08T19:00:00Z');
const event=(seconds,icao,altitudeFeet)=>({milliseconds:start+seconds*1000,aircraft:{icao,altitudeFeet}});

test('builds a snapshot from each aircraft latest saved event',()=>{
 const events=[event(0,'AAA111',1000),event(1,'BBB222',2000),event(2,'AAA111',3000)];
 const snapshot=snapshotAt(events,start+2500,5*60*1000);
 assert.deepEqual(snapshot,[{icao:'AAA111',altitudeFeet:3000},{icao:'BBB222',altitudeFeet:2000}]);
});

test('expires aircraft that had no saved events in the live expiry window',()=>{
 const events=[event(0,'AAA111',1000),event(301,'BBB222',2000)];
 assert.deepEqual(snapshotAt(events,start+301000,5*60*1000),[{icao:'BBB222',altitudeFeet:2000}]);
});

test('clamps playback time to the loaded history bounds',()=>{
 assert.equal(clampCursor(start-1000,start,start+5000),start);
 assert.equal(clampCursor(start+2000,start,start+5000),start+2000);
 assert.equal(clampCursor(start+6000,start,start+5000),start+5000);
});
