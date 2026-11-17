package main

import (
	"os/exec"
	"testing"
)

func TestTrafficViewsClockChoices(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	t.Setenv("TZ", "UTC")
	code := sliceBetween(t, indexHTML, "function EPR(", "function servedPanelHTML(")
	clock := sliceBetween(t, indexHTML, "let clockChoice=", "const fmtTime=")
	runNode(t, node, `process.env.TZ='UTC'; const window={}; let D;
const localStorage={getItem(){return null},setItem(){}};
let rendered; function svFill(){rendered=views()}; function renderWalletTable(){} function renderMountedTheme(){} function ufRefreshSample(){}`+clock+code+`
const start=Date.UTC(2030,0,1), iv={kind:'interval',fromMillis:start+15*3600000,toMillis:start+16*3600000,bytes:3600};
const data=tpBuckets([iv],start+18*3600000);
function views(){return [tpClock(data),tpHeatmap(data),tpRecords([iv],data,[])];}
const assert=(b,m)=>{if(!b)throw Error(m)};
for(const [mode,hour,next,midnight] of [['12h','3:00 PM','4:00 PM','12:00 AM'],['24h','15:00','16:00','00:00'],['auto','15:00','16:00','00:00']]){
 clockSet(mode);
 const [clock,heat,records]=rendered;
 assert(clock.includes('>'+hour+'</text>'),mode+' busiest');
 assert(clock.includes(hour+' - '+next),mode+' clock tooltip');
 assert(clock.includes('>'+midnight+'</text>'),mode+' midnight axis');
 assert(heat.includes('>'+hour+'</span>')&&heat.includes(' '+hour+': '),mode+' heatmap axis/tooltip');
 assert(records.includes(hour+' - ')&&records.includes(next),mode+' records');
 assert(tpHour(23)!==tpHour(0),mode+' day boundary');
}
`)
}
