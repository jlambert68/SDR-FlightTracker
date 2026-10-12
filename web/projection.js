(function(root){
 const earthRadiusMeters=6371008.8;
 const metersPerNauticalMile=1852;
 const radiansPerDegree=Math.PI/180;
 const degreesPerRadian=180/Math.PI;

 function captureMotion(currentMotion,speedKnots,headingDegrees){
  if(currentMotion)return currentMotion;
  if(speedKnots==null||headingDegrees==null)return null;
  const speed=Number(speedKnots),heading=Number(headingDegrees);
  if(!Number.isFinite(speed)||speed<0||!Number.isFinite(heading))return null;
  const normalizedHeading=heading%360;
  return Object.freeze({speedKnots:speed,headingDegrees:normalizedHeading<0?normalizedHeading+360:normalizedHeading});
 }

 function positionAfterSeconds(origin,speedKnots,headingDegrees,elapsedSeconds){
  if(origin?.latitude==null||origin?.longitude==null||origin?.altitudeFeet==null||speedKnots==null||headingDegrees==null||elapsedSeconds==null)return null;
  const latitude=Number(origin?.latitude),longitude=Number(origin?.longitude),altitudeFeet=Number(origin?.altitudeFeet);
  if(!Number.isFinite(latitude)||latitude<-90||latitude>90||!Number.isFinite(longitude)||longitude<-180||longitude>180||!Number.isFinite(altitudeFeet)||!Number.isFinite(speedKnots)||speedKnots<0||!Number.isFinite(headingDegrees)||!Number.isFinite(elapsedSeconds)||elapsedSeconds<0)return null;
  const angularDistance=speedKnots*metersPerNauticalMile*elapsedSeconds/3600/earthRadiusMeters;
  const latitude1=latitude*radiansPerDegree,longitude1=longitude*radiansPerDegree,bearing=headingDegrees*radiansPerDegree;
  const latitude2=Math.asin(Math.sin(latitude1)*Math.cos(angularDistance)+Math.cos(latitude1)*Math.sin(angularDistance)*Math.cos(bearing));
  const longitude2=longitude1+Math.atan2(Math.sin(bearing)*Math.sin(angularDistance)*Math.cos(latitude1),Math.cos(angularDistance)-Math.sin(latitude1)*Math.sin(latitude2));
  return{latitude:latitude2*degreesPerRadian,longitude:(longitude2*degreesPerRadian+540)%360-180,altitudeFeet};
 }

 function positionAlongProjection(origin,speedKnots,headingDegrees,elapsedSeconds,stepSeconds=5){
  const elapsed=Number(elapsedSeconds),step=Number(stepSeconds);
  if(!Number.isFinite(elapsed)||elapsed<0||!Number.isFinite(step)||step<=0)return null;
  let projected=positionAfterSeconds(origin,speedKnots,headingDegrees,0);
  if(!projected)return null;
  const completeSteps=Math.floor(elapsed/step);
  for(let index=0;index<completeSteps;index++)projected=positionAfterSeconds(projected,speedKnots,headingDegrees,step);
  const remainder=elapsed-completeSteps*step;
  return remainder>1e-9?positionAfterSeconds(projected,speedKnots,headingDegrees,remainder):projected;
 }

 function surfaceDistanceMeters(first,second){
  const latitude1=Number(first?.latitude),longitude1=Number(first?.longitude),latitude2=Number(second?.latitude),longitude2=Number(second?.longitude);
  if(!Number.isFinite(latitude1)||latitude1<-90||latitude1>90||!Number.isFinite(longitude1)||longitude1<-180||longitude1>180||!Number.isFinite(latitude2)||latitude2<-90||latitude2>90||!Number.isFinite(longitude2)||longitude2<-180||longitude2>180)return null;
  const latitude1Radians=latitude1*radiansPerDegree,latitude2Radians=latitude2*radiansPerDegree;
  const deltaLatitude=(latitude2-latitude1)*radiansPerDegree,deltaLongitude=(longitude2-longitude1)*radiansPerDegree;
  const haversine=Math.sin(deltaLatitude/2)**2+Math.cos(latitude1Radians)*Math.cos(latitude2Radians)*Math.sin(deltaLongitude/2)**2,boundedHaversine=Math.min(1,Math.max(0,haversine));
  return earthRadiusMeters*2*Math.atan2(Math.sqrt(boundedHaversine),Math.sqrt(1-boundedHaversine));
 }

 function historyDeviationMeters(origin,motion,originTimestamp,historyPoint,stepSeconds=5){
  const originMilliseconds=Date.parse(originTimestamp),historyMilliseconds=Number.isFinite(historyPoint?.milliseconds)?Number(historyPoint.milliseconds):Date.parse(historyPoint?.timestamp);
  if(!motion||!Number.isFinite(originMilliseconds)||!Number.isFinite(historyMilliseconds)||historyMilliseconds<=originMilliseconds)return null;
  const projected=positionAlongProjection(origin,motion.speedKnots,motion.headingDegrees,(historyMilliseconds-originMilliseconds)/1000,stepSeconds);
  return projected?surfaceDistanceMeters(projected,historyPoint):null;
 }

 function historyDivergesFromProjection(origin,motion,originTimestamp,historyPoints,limitMeters,stepSeconds=5){
  const limit=Number(limitMeters);
  if(!Array.isArray(historyPoints)||!Number.isFinite(limit)||limit<0)return false;
  return historyPoints.some(point=>{const deviation=historyDeviationMeters(origin,motion,originTimestamp,point,stepSeconds);return deviation!==null&&deviation>=limit});
 }

 root.FlightProjection=Object.freeze({captureMotion,historyDeviationMeters,historyDivergesFromProjection,positionAfterSeconds,positionAlongProjection,surfaceDistanceMeters});
})(globalThis);
