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
const start=Date.UTC(2030,0,1), iv={kind:'interval',fromMillis:start+15*3600000,toMillis:start+16*3600000,bytes:3600,bytesPerSec:1};
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
 const chart=svChart([iv],'all');
 assert(chart.includes('title="01-01 '+hour+' → 01-01 '+next),mode+' rate chart tooltip');
 assert(chart.includes('<span>01-01 '+hour+'</span>')&&chart.includes('<span>01-01 '+next+'</span>'),mode+' rate chart axis');
 assert(tpHour(23)!==tpHour(0),mode+' day boundary');
}
`)
}

func TestPriceHoverClockChoices(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	t.Setenv("TZ", "UTC")
	t.Setenv("LANG", "en_US.UTF-8")
	clock := funcBody(t, indexHTML, "function clockFormat(")
	hover := funcBody(t, indexHTML, "function sparkHoverInit(")
	runNode(t, node, `process.env.TZ='UTC'; let clockChoice='auto'; const events={};
const tip={style:{}}, guide={setAttribute(){}}, dot={setAttribute(){}};
const plot={dataset:{}}, svg={getBoundingClientRect(){return {left:0,width:100}},addEventListener(n,f){events[n]=f}};
const elements={'spark-plot':plot,'alpha-spark':svg,'spark-tip':tip,'spark-guide':guide,'spark-hover-dot':dot};
const document={getElementById(id){return elements[id]}};
const window={__sparkSeries:{vals:[1,2],xy:[[0,0],[100,10]],pts:[{t:Date.UTC(2030,0,1,15)/1000},{t:Date.UTC(2030,0,1,16)/1000}]}};`+clock+hover+`
sparkHoverInit();
for(const [mode,expected] of [['12h','03:00 PM'],['24h','15:00'],['auto','03:00 PM']]){
clockChoice=mode; events.mousemove({clientX:0});
const text=tip.textContent.replace(/[\u202f\u00a0]/g,' ');
if(!text.includes(expected))throw Error(mode+': '+text);
}
`)
}
