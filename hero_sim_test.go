package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestIndexHTML_HeroMarkers(t *testing.T) {
	for _, s := range []string{"const HERO_TRAIL_LENGTH=10", "const HERO_PARTICLE_CAP=180", ".hero-sim{position:relative;height:400px", "height:260px", "if(heroMode==='off')"} {
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
