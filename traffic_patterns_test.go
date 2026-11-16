package main

import (
	"os/exec"
	"testing"
)

func TestTrafficPatterns(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	t.Setenv("TZ", "UTC")
	code := sliceBetween(t, indexHTML, "function EPR(", "function servedPanelHTML(")
	runNode(t, node, `process.env.TZ='UTC'; let clockChoice='24h'; const window={}; let D;`+funcBody(t, indexHTML, "function clockFormat(")+code+`
const check=(condition,message)=>{if(!condition) throw Error(message)};
const start=Date.UTC(2030,0,6); // Sunday
const iv=(from,to,bytes,kind='interval')=>({fromMillis:from,toMillis:to,bytes,kind});
let b=tpBuckets([iv(start+1800000,start+5400000,3600)],start+7200000);
check(Math.abs(b.hours[0].bytes-1800)<1e-8 && Math.abs(b.hours[1].bytes-1800)<1e-8,'split at hour');
check(b.hours[0].ms===1800000 && b.hours[1].ms===1800000,'coverage');
b=tpBuckets([iv(start,start+3600000,0),iv(start+3600000,start+7200000,100,'rollover'),iv(start,start+14400000,100)],start+14400000);
check(b.hours[0].ms===3600000 && b.hours[1].ms===0,'zero, gaps and long intervals');
check(tpCoverage([iv(start,start+3600000,50)],start,start+3600000).bytes===50,'complete interval');
check(tpCoverage([iv(start+10000,start+3600000,50)],start,start+3600000)===null,'gap rejected');
const wk=7*86400000, prev=start-wk;
const html=tpComparison([iv(prev,prev+3600000,100),iv(start,start+3600000,150)],start+3600000);
check(html.includes('50.0% ahead') && html.includes('1.0 of 1.0 hours matched'),'matched comparison');
check(tpComparison([iv(start,start+3600000,150)],start+3600000).includes('No matching'),'missing history');
const gaps=tpComparison([iv(prev,prev+3600000,100),iv(prev+7200000,prev+10800000,100),iv(start,start+3600000,100),iv(start+7200000,start+10800000,100)],start+10800000);
check((gaps.match(/class="tp-current"/g)||[]).length===2,'comparison breaks at gaps');
const missingDay=tpBuckets([iv(start,start+3600000,100)],start+3*86400000);
check(missingDay.days.some(d=>d.start===start+86400000 && d.ms===0),'missing day retained');
process.env.TZ='America/Los_Angeles';
const spring=Date.parse('2030-03-10T09:30:00Z');
const springData=tpBuckets([iv(spring,spring+3600000,3600)],spring+3600000);
check(springData.hours[1].ms===1800000 && springData.hours[2].ms===0 && springData.hours[3].ms===1800000,'spring daylight saving');
const dstStart=Date.parse('2030-03-10T09:00:00Z'), oldStart=Date.parse('2030-03-03T09:00:00Z');
check(tpComparison([iv(oldStart,oldStart+7200000,200),iv(dstStart,dstStart+3600000,100)],dstStart+3600000).includes('No matching'),'unequal DST durations excluded');
const autumn=Date.parse('2030-11-03T08:30:00Z');
const autumnData=tpBuckets([iv(autumn,autumn+3600000,3600)],autumn+3600000);
check(autumnData.hours[1].ms===3600000,'fall repeated hour');
process.env.TZ='UTC';
check(tpHeatmap(tpBuckets([],start)).includes('No measured'),'empty heatmap');
check(tpClock(tpBuckets([],start)).includes('needs measured'),'empty clock');
`)
}
