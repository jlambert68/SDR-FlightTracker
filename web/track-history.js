(function(root){
 function pointTime(point){
  if(Number.isFinite(point?.milliseconds))return Number(point.milliseconds);
  return Date.parse(point?.timestamp);
 }

 function normalizeHistoryPoint(point){
  const milliseconds=pointTime(point),latitude=Number(point?.latitude),longitude=Number(point?.longitude),altitudeFeet=Number(point?.altitudeFeet);
  if(!Number.isFinite(milliseconds)||!Number.isFinite(latitude)||latitude<-90||latitude>90||!Number.isFinite(longitude)||longitude<-180||longitude>180||!Number.isFinite(altitudeFeet))return null;
  return{timestamp:point.timestamp,milliseconds,latitude,longitude,altitudeFeet};
 }

 function mergeHistoryPoints(existing,incoming,before){
  const beforeMilliseconds=Date.parse(before),byTimestamp=new Map();
  for(const point of existing||[]){const normalized=normalizeHistoryPoint(point);if(normalized)byTimestamp.set(normalized.milliseconds,normalized)}
  for(const point of incoming||[]){const normalized=normalizeHistoryPoint(point);if(normalized&&(!Number.isFinite(beforeMilliseconds)||normalized.milliseconds<beforeMilliseconds))byTimestamp.set(normalized.milliseconds,normalized)}
  return Array.from(byTimestamp.values()).sort((first,second)=>first.milliseconds-second.milliseconds);
 }

 function splitAtGaps(points,gapMilliseconds){
  const sorted=(points||[]).filter(point=>Number.isFinite(pointTime(point))).slice().sort((first,second)=>pointTime(first)-pointTime(second));
  const segments=[];let current=[];
  for(const point of sorted){
   if(current.length&&pointTime(point)-pointTime(current[current.length-1])>=gapMilliseconds){if(current.length>1)segments.push(current);current=[]}
   current.push(point);
  }
  if(current.length>1)segments.push(current);
  return segments;
 }

 function segmentCovers(segments,start,end){return segments.some(segment=>pointTime(segment[0])<=start&&pointTime(segment[segment.length-1])>=end)}
 function appendFallback(segments,current,first,second){
  if(current&&current[current.length-1]===first){current.push(second);return current}
  current=[first,second];segments.push(current);return current;
 }

 function unresolvedSignalSegments(signals,historyPoints,gapMilliseconds){
  const orderedSignals=(signals||[]).filter(signal=>Number.isFinite(pointTime(signal))).slice().sort((first,second)=>pointTime(first)-pointTime(second));
  if(!orderedSignals.length)return[];
  const historySegments=splitAtGaps(historyPoints,gapMilliseconds),fallback=[];let current=null;
  const firstSignal=orderedSignals[0],firstTime=pointTime(firstSignal);
  const preceding=(historyPoints||[]).filter(point=>Number.isFinite(pointTime(point))&&pointTime(point)<firstTime).sort((first,second)=>pointTime(first)-pointTime(second)),precedingHistory=preceding[preceding.length-1];
  if(precedingHistory&&firstTime-pointTime(precedingHistory)<gapMilliseconds&&!segmentCovers(historySegments,firstTime,firstTime))current=appendFallback(fallback,current,precedingHistory,firstSignal);
  for(let index=1;index<orderedSignals.length;index++){
   const previous=orderedSignals[index-1],next=orderedSignals[index],start=pointTime(previous),end=pointTime(next);
   if(end-start>=gapMilliseconds||segmentCovers(historySegments,start,end)){current=null;continue}
   current=appendFallback(fallback,current,previous,next);
  }
  return fallback;
 }

 function refreshBefore(signal,observedMilliseconds,nowMilliseconds,liveWindowMilliseconds){
  const signalMilliseconds=pointTime(signal),observed=Number(observedMilliseconds),now=Number(nowMilliseconds);
  if(!Number.isFinite(signalMilliseconds)||!Number.isFinite(observed)||!Number.isFinite(now)||!Number.isFinite(liveWindowMilliseconds))return null;
  const end=Math.abs(observed-signalMilliseconds)<=liveWindowMilliseconds?Math.max(signalMilliseconds,now):signalMilliseconds+Math.max(0,now-observed);
  return new Date(end).toISOString();
 }

 root.FlightTrackHistory=Object.freeze({mergeHistoryPoints,refreshBefore,splitAtGaps,unresolvedSignalSegments});
})(globalThis);
