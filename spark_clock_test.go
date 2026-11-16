package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestIndexHTML_SparkClockMarkers(t *testing.T) {
	for _, marker := range []string{`id="clock-select"`, `function clockFormat(`, `function svSparkline(`, `class="srvd-spark"`, `aria-hidden="true"`} {
		if !strings.Contains(indexHTML, marker) {
			t.Errorf("missing %s", marker)
		}
	}
}

func TestSparklineLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := sliceBetween(t, indexHTML, "function svSparkline(", "function svRate(")
	runNode(t, node, code+`
const eq=(a,b)=>{if(a!==b) throw Error(JSON.stringify(a)+' != '+JSON.stringify(b))};
eq(svSparkline([0], 10),'');
eq(svSparkline([0,5,10],10),'▁▅█');
eq(svSparkline([0,0],0),'▁▁');
eq(svSparkline([5,10],20),'▃▅');
eq(svSparkline([100,5],10),'█▅');
`)
}

func TestSparklineRows(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := sliceBetween(t, indexHTML, "function EPR(", "/* The three figures that lead")
	runNode(t, node, `const window={}; let D; let clockChoice='auto';`+funcBody(t, indexHTML, "function clockFormat(")+code+`
const ivs=[{kind:'interval',bytesPerSec:0},{kind:'rollover',bytesPerSec:999},{kind:'interval',bytesPerSec:5},{kind:'revised',bytesPerSec:999},{kind:'interval',bytesPerSec:10}].map((x,i)=>({...x,bytes:100,fromMillis:1893456000000+i*3600000,toMillis:1893459600000+i*3600000}));
D={served:{block:{asOfMillis:1893474000000,blockStartMillis:1893456000000},intervals:ivs}};
const html=servedPanelHTML(D,false); const table=html.split('Counted between (')[1].split('</table>')[0];
const rows=table.match(/<tr[^>]*>.*?<\/tr>/g);
if(!rows[0].includes('▁▅█') || !rows[2].includes('▁▅') || rows[1].includes('srvd-spark') || rows[3].includes('srvd-spark') || rows[4].includes('srvd-spark')) throw Error(rows.join('\n'));
const many=Array.from({length:30},(_,i)=>({kind:'interval',bytesPerSec:i,bytes:i,fromMillis:1893456000000+i*3600000,toMillis:1893459600000+i*3600000}));
D.served.intervals=many;
const table2=servedPanelHTML(D,false).split('Counted between (')[1].split('</table>')[0];
const strips=[...table2.matchAll(/class="srvd-spark"[^>]*>([^<]+)/g)];
if(strips.length!==24 || strips[0][1].length!==12 || strips[23][1].length!==7) throw Error('history length');
`)
}

func TestClockStorageBlocked(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := sliceBetween(t, indexHTML, "let clockChoice=", "const fmtTime=")
	runNode(t, node, `const localStorage={getItem(){throw Error('blocked')},setItem(){throw Error('blocked')}};
let calls=0; const svFill=()=>calls++, renderWalletTable=()=>calls++, renderMountedTheme=()=>calls++, ufRefreshSample=()=>calls++;`+code+`
clockSet('24h');
if(clockChoice!=='24h'||calls!==4) throw Error('blocked storage broke selection');
if(!clockFormat(new Date('2030-01-02T15:04:00Z'),'en-US',{timeZone:'UTC',hour:'numeric',minute:'2-digit'}).includes('15:04')) throw Error('format not applied');
`)
}

func TestClockLegacyOutput(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	t.Setenv("LANG", "de_DE.UTF-8")
	clock := funcBody(t, indexHTML, "function clockFormat(")
	sv := funcBody(t, indexHTML, "function svTime(")
	if strings.Contains(indexHTML, "function localClockTime(") {
		sv += funcBody(t, indexHTML, "function localClockTime(")
	}
	fmt := sliceBetween(t, indexHTML, "const fmtTime=", "/* a window is only late")
	lag := funcBody(t, indexHTML, "function lagNote(")
	refresh := funcBody(t, indexHTML, "async function ufRefreshSample(")
	runNode(t, node, `process.env.TZ='UTC'; let clockChoice='auto'; const TZ='UTC', LATE_MS=120000;
const SV_P=n=>String(n).padStart(2,'0');
let el={textContent:''}; const document={querySelectorAll:()=>[el]};
const fetch=async()=>({ok:true,json:async()=>({unix:1893510240})});
const ufLocalize=()=>{};`+clock+sv+fmt+lag+refresh+`
const failures=[]; const eq=(label,a,b)=>{if(a!==b) failures.push(label+': '+JSON.stringify(a)+' != '+JSON.stringify(b))};
(async()=>{
const instant='2030-01-01T15:04:00Z';
for(const [mode,svExpected,fmtExpected,sampleExpected,lagExpected] of [
 ['auto','01-01 15:04','Jan 1, 2030, 3:04:00 PM','today at 15:04','03:04 PM'],
 ['12h','01-01 3:04 PM','Jan 1, 2030, 3:04:00 PM','today at 3:04 PM','03:04 PM'],
 ['24h','01-01 15:04','Jan 1, 2030, 15:04:00','today at 15:04','15:04']]){
 clockChoice=mode;
 eq(mode+' svTime',svTime(Date.parse(instant)),svExpected);
 eq(mode+' fmtTime',fmtTime(instant),fmtExpected);
 await ufRefreshSample(); eq(mode+' today at',el.textContent,sampleExpected);
 eq(mode+' lagNote',lagNote('2030-01-01T15:00:00Z',instant).includes('fetched '+lagExpected),true);
}
if(failures.length) throw Error(failures.join('\n'));
clockChoice='12h';
eq('midnight',svTime(Date.parse('2030-01-01T00:04:00Z')),'01-01 12:04 AM');
eq('noon',svTime(Date.parse('2030-01-01T12:04:00Z')),'01-01 12:04 PM');
if(failures.length) throw Error(failures.join('\n'));
})().catch(e=>{console.error(e);process.exitCode=1});`)
}

func TestClockFormatLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := sliceBetween(t, indexHTML, "function clockFormat(", "function clockSet(")
	runNode(t, node, code+`
const d=new Date('2030-01-02T15:04:00Z'), opts={timeZone:'UTC',hour:'numeric',minute:'2-digit'};
const a=clockFormat(d,'en-US',opts,'12h').replace(/[\u202f\u00a0]/g,' '), b=clockFormat(d,'en-US',opts,'24h');
if(a!=='3:04 PM'||b!=='15:04') throw Error(a+' / '+b);
if(clockFormat(d,'en-US',opts,'auto')!==d.toLocaleString('en-US',opts)) throw Error('auto');
`)
}
