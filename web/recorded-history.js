(function(root){
 function eventTime(event){
  const milliseconds=Number(event?.milliseconds);
  return Number.isFinite(milliseconds)?milliseconds:Date.parse(event?.timestamp);
 }

 function snapshotAt(events,cursorMilliseconds,expiryMilliseconds){
  const cursor=Number(cursorMilliseconds),expiry=Number(expiryMilliseconds),latest=new Map();
  if(!Number.isFinite(cursor)||!Number.isFinite(expiry)||expiry<0)return[];
  for(const event of events||[]){
   const milliseconds=eventTime(event);
   if(!Number.isFinite(milliseconds))continue;
   if(milliseconds>cursor)break;
   const aircraft=event?.aircraft,icao=aircraft?.icao;
   if(typeof icao==='string'&&icao)latest.set(icao,{milliseconds,aircraft});
  }
  return Array.from(latest.values()).filter(item=>cursor-item.milliseconds<=expiry).map(item=>item.aircraft);
 }

 function clampCursor(value,start,end){
  const cursor=Number(value),minimum=Number(start),maximum=Number(end);
  if(!Number.isFinite(minimum)||!Number.isFinite(maximum))return null;
  if(!Number.isFinite(cursor))return minimum;
  return Math.min(maximum,Math.max(minimum,cursor));
 }

 root.RecordedHistory=Object.freeze({clampCursor,eventTime,snapshotAt});
})(globalThis);
