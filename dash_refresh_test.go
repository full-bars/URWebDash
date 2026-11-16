package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestIndexHTML_DashboardDataMutable(t *testing.T) {
	if strings.Contains(indexHTML, "const D =") || !strings.Contains(indexHTML, "let D = __D__;") {
		t.Fatal("dashboard data must be replaceable on refresh")
	}
}

func TestDashboardRefresh(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	start := strings.Index(indexHTML, " D = __D__;")
	if start < 0 {
		t.Fatal("dashboard data declaration missing")
	}
	lineStart := strings.LastIndex(indexHTML[:start], "\n") + 1
	declaration := strings.Replace(indexHTML[lineStart:start+len(" D = __D__;")], "__D__", `{old:true}`, 1)
	refresh := sliceBetween(t, indexHTML, "let dashPolling=", "setInterval(()=>{ if(!document.hidden) refreshDashData(); }")
	runNode(t, node, declaration+`
const elements={'c-paid':{},'c-unpaid':{},'c-total':{}};
const document={hidden:false,getElementById:id=>elements[id]||null};
let epLive=null, calls=[];
const updateEpochSub=()=>calls.push('epoch'), renderPrice=()=>calls.push('price'), renderMountedTheme=()=>calls.push('theme');
const fmtBytes=x=>String(x);
let fail=false, warnings=0;
console.warn=(...args)=>{if(args[0]!=='dashboard refresh failed') throw Error('wrong warning'); warnings++};
const payload={epoch:{n:42,pct:10},epochs:[],points:{},wallet:{},alpha:{},legacy:{},traffic:{paidBytes:1,unpaidBytes:2}};
let response=payload;
const fetch=async()=>{if(fail) throw Error('offline'); return {ok:true,json:async()=>response}};
`+refresh+`
(async()=>{
 await refreshDashData();
 if(D!==payload || calls.join(',')!=='epoch,price,theme') throw Error('data or renders not refreshed: '+JSON.stringify({D,calls}));
 if(elements['c-paid'].textContent!=='1' || elements['c-unpaid'].textContent!=='2' || elements['c-total'].textContent!=='3') throw Error('traffic cards not refreshed');
 fail=true; await refreshDashData(); await refreshDashData();
 if(warnings!==1 || dashPolling) throw Error('failure streak not reported once');
 fail=false; await refreshDashData(); fail=true; await refreshDashData();
 if(warnings!==2) throw Error('warning did not reset after recovery');
 fail=false; await refreshDashData();
 for(const invalid of [{...payload,epoch:null},{...payload,traffic:null},{...payload,epoch:{}},{...payload,epochs:null}]){
   response=invalid; const previous=D; const before=calls.length;
   await refreshDashData();
   if(D!==previous || calls.length!==before) throw Error('invalid data replaced the last good payload');
 }
 if(warnings!==3) throw Error('invalid data streak was not reported once');
 response=payload; await refreshDashData();
 if(dashRefreshFailed) throw Error('valid data did not clear failure streak');
})().catch(e=>{console.error(e);process.exitCode=1});`)
}
