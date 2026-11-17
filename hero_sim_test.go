package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestIndexHTML_HeroMarkers(t *testing.T) {
	for _, s := range []string{"const HERO_TRAIL_LENGTH=10", "const HERO_PARTICLE_CAP=180", ".hero-sim{position:relative;height:400px", "height:260px"} {
		if !strings.Contains(indexHTML, s) {
			t.Errorf("missing %s", s)
		}
	}
	sim := sliceBetween(t, indexHTML, "function startHeroSim(", "window.__heroStart=startHeroSim;")
	for _, s := range []string{"ctx.fillText(", "ctx.strokeText("} {
		if strings.Contains(sim, s) {
			t.Errorf("unexpected label %s", s)
		}
	}
}

func TestIndexHTML_HeroMotionMath(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := funcBody(t, indexHTML, "function heroDt(") + funcBody(t, indexHTML, "function heroSpawnChance(") + funcBody(t, indexHTML, "function heroSpawnCount(")
	runNode(t, node, code+`
const near=(a,b,e=1e-9)=>{if(Math.abs(a-b)>e)throw Error(a+' != '+b)};
let a=0,b=0; for(let i=0;i<60;i++)a+=heroDt(1000/60); for(let i=0;i<144;i++)b+=heroDt(1000/144); near(a,b);
if(heroSpawnCount(.25,3,()=>0)!==3)throw Error('multiple spawns');
if(heroSpawnCount(.25,.5,()=>.2)!==0 || heroSpawnCount(.25,.5,()=>.1)!==1)throw Error('fractional frame');
near(heroDt(1000),3); near(heroDt(-1),0);
near(1-Math.pow(1-heroSpawnChance(.01,60/144),144),1-Math.pow(1-heroSpawnChance(.01,1),60));
`)
}

func heroStyleHarness(t *testing.T) (string, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	styles := sliceBetween(t, indexHTML, "const currentHeroStyle=", "function startHeroSim(")
	return node, styles
}

func TestHeroFixedStyleAndControls(t *testing.T) {
	node, code := heroStyleHarness(t)
	for _, s := range []string{`id="hero-controls"`, `id="hero-mode"`, `id="hero-streaks"`, "heroMode", "heroStreaks"} {
		if strings.Contains(indexHTML, s) {
			t.Errorf("obsolete control or storage key: %s", s)
		}
	}
	runNode(t, node, `const localStorage={getItem(k){return {heroMode:'off',heroStreaks:'soft'}[k]},setItem(){throw Error('unexpected storage write')}};`+code+`
if(JSON.stringify(currentHeroStyle)!==JSON.stringify({width:2,tail:.2,head:1,headSize:1.5,glowSize:3,glowAlpha:.2}))throw Error('bright values');`)
}

func TestHeroStyleNoPacketShadowBlur(t *testing.T) {
	_, code := heroStyleHarness(t)
	draw := funcBody(t, code, "function drawHeroTrail(")
	loop := sliceBetween(t, indexHTML, "for(const pt of parts){", "for(const pt of finished){")
	if strings.Contains(draw, "shadowBlur") || strings.Contains(loop, "shadowBlur") {
		t.Fatal("packet trail uses shadowBlur")
	}
	if !strings.Contains(loop, "drawHeroTrail(ctx,pt,") {
		t.Fatal("packet loop must use tested drawing function")
	}
}

func TestHeroStyleDrawingCalls(t *testing.T) {
	node, code := heroStyleHarness(t)
	runNode(t, node, code+`
const record=()=>{const calls=[]; const ctx=new Proxy({}, {set(_,p,v){calls.push([p,v]);return true},get(_,p){return (...a)=>calls.push([p,...a])}});return {ctx,calls}};
const pt={color:'#abc',trail:[{x:1,y:2},{x:3,y:4},{x:5,y:6}]};
const a=record(); drawHeroTrail(a.ctx,pt,currentHeroStyle);
// Recorded from the default bright path at v0.0.26.
const expected=[['strokeStyle','#abc'],['lineWidth',2],['globalAlpha',.2],['beginPath'],['moveTo',1,2],['lineTo',3,4],['stroke'],['globalAlpha',1],['beginPath'],['moveTo',3,4],['lineTo',5,6],['stroke'],['globalAlpha',1],['fillStyle','#abc'],['globalAlpha',.2],['beginPath'],['arc',5,6,3,0,Math.PI*2],['fill'],['globalAlpha',1],['beginPath'],['arc',5,6,1.5,0,Math.PI*2],['fill'],['globalAlpha',1]];
if(JSON.stringify(a.calls)!==JSON.stringify(expected))throw Error('bright drawing changed: '+JSON.stringify(a.calls));
`)
}

func TestIndexHTML_HeroDensityMath(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := funcBody(t, indexHTML, "function heroSpawnChance(") + funcBody(t, indexHTML, "function heroDensity(") + funcBody(t, indexHTML, "function heroEase(")
	runNode(t, node, code+`
const eq=(a,b)=>{if(a!==b)throw Error(a+' != '+b)};
eq(heroDensity([]),1); eq(heroDensity([0,0]),1); eq(heroDensity([1,1,10]),3); eq(heroDensity([10,10,1]),.5);
let x=heroEase(1,3,1); if(!(x>1&&x<3))throw Error(x); let y=heroEase(x,3,1); if(!(y>x&&y<3))throw Error(y);
`)
}
