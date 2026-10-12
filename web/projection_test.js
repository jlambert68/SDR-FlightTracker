const test=require('node:test');
const assert=require('node:assert/strict');

require('./projection.js');

const {captureMotion,historyDeviationMeters,historyDivergesFromProjection,positionAfterSeconds,positionAlongProjection,surfaceDistanceMeters}=globalThis.FlightProjection;

test('projects five seconds from speed and heading without changing altitude',()=>{
 const origin={latitude:59.323316,longitude:18.480349,altitudeFeet:9825};
 const projected=positionAfterSeconds(origin,360,90,5);
 assert.ok(projected);
 assert.ok(Math.abs(surfaceDistanceMeters(origin,projected)-926)<0.01);
 assert.ok(projected.longitude>origin.longitude);
 assert.ok(Math.abs(projected.latitude-origin.latitude)<0.00001);
 assert.equal(projected.altitudeFeet,origin.altitudeFeet);
});

test('chains projection intervals without mutating the received position',()=>{
 const origin={latitude:59.37290954589844,longitude:18.16827392578125,altitudeFeet:9825};
 const afterFive=positionAfterSeconds(origin,332.86633954186476,4.13467137373641,5);
 const afterTen=positionAfterSeconds(afterFive,332.86633954186476,4.13467137373641,5);
 assert.ok(Math.abs(surfaceDistanceMeters(origin,afterFive)-856.206)<0.02);
 assert.ok(Math.abs(surfaceDistanceMeters(origin,afterTen)-1712.412)<0.02);
 assert.deepEqual(origin,{latitude:59.37290954589844,longitude:18.16827392578125,altitudeFeet:9825});
});

test('keeps the motion captured at a received position for the whole sequence',()=>{
 const motion=captureMotion(null,228.3,272.8);
 const afterVelocityOnlyUpdate=captureMotion(motion,231.6,196.6);
 const origin={latitude:59.54567,longitude:18.16763,altitudeFeet:6475};
 const afterFive=positionAfterSeconds(origin,motion.speedKnots,motion.headingDegrees,5);
 const afterTen=positionAfterSeconds(afterFive,afterVelocityOnlyUpdate.speedKnots,afterVelocityOnlyUpdate.headingDegrees,5);
 assert.strictEqual(afterVelocityOnlyUpdate,motion);
 assert.deepEqual(motion,{speedKnots:228.3,headingDegrees:272.8});
 assert.ok(afterFive.latitude>origin.latitude);
 assert.ok(afterTen.latitude>afterFive.latitude);
});

test('rejects incomplete motion data',()=>{
 const origin={latitude:59.3,longitude:18.4,altitudeFeet:10000};
 assert.equal(captureMotion(null,undefined,90),null);
 assert.equal(captureMotion(null,300,null),null);
 assert.equal(positionAfterSeconds(origin,undefined,90,5),null);
 assert.equal(positionAfterSeconds(origin,300,null,5),null);
 assert.equal(positionAfterSeconds(origin,300,undefined,5),null);
 assert.equal(positionAfterSeconds(origin,-1,90,5),null);
});

test('stops an EI-SIK projection when time-matched internet history is 500 metres away',()=>{
 const origin={latitude:59.69663458355402,longitude:17.927698595770472,altitudeFeet:2875};
 const originTimestamp='2026-10-12T19:06:18.731Z';
 const historyTimestamp='2026-10-12T19:07:18.731Z';
 const motion=captureMotion(null,204.56050449683585,312.4244174436837);
 const projected=positionAlongProjection(origin,motion.speedKnots,motion.headingDegrees,60,5);
 const secondsPerMeter=3600/(360*1852);
 const near={...positionAfterSeconds(projected,360,90,499*secondsPerMeter),timestamp:historyTimestamp};
 const limit={...positionAfterSeconds(projected,360,90,500*secondsPerMeter),timestamp:historyTimestamp};
 const old={...limit,timestamp:originTimestamp};

 assert.ok(historyDeviationMeters(origin,motion,originTimestamp,near)<500);
 assert.ok(Math.abs(historyDeviationMeters(origin,motion,originTimestamp,limit)-500)<0.001);
 assert.equal(historyDivergesFromProjection(origin,motion,originTimestamp,[near],500),false);
 assert.equal(historyDivergesFromProjection(origin,motion,originTimestamp,[near,limit],500),true);
 assert.equal(historyDivergesFromProjection(origin,motion,originTimestamp,[old],500),false);
});

test('history comparison reconstructs the same five-second steps as the displayed dots',()=>{
 const origin={latitude:59.7,longitude:17.9,altitudeFeet:3000};
 let displayed=origin;
 for(let elapsed=0;elapsed<600;elapsed+=5)displayed=positionAfterSeconds(displayed,205,90,5);
 const reconstructed=positionAlongProjection(origin,205,90,600,5);
 const oneLongGeodesic=positionAfterSeconds(origin,205,90,600);

 assert.ok(surfaceDistanceMeters(displayed,reconstructed)<0.001);
 assert.ok(surfaceDistanceMeters(displayed,oneLongGeodesic)>500);
});
