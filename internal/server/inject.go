package server

import (
	"bytes"
	"encoding/json"
	"strings"
)

// fetchShimJS is ported verbatim from server.py's _FETCH_SHIM_JS. The %s is
// replaced with the JSON-encoded token (json.Marshal of a string yields the same
// quoted literal as Python's json.dumps).
//
// Invariant: every /api/ access must go through fetch / XMLHttpRequest. There is
// no EventSource/WebSocket use in the frontend, so this shim covers the surface.
const fetchShimJS = `(function(){
var T=%s;
window.__DEVHUB_TOKEN__=T;
function isApi(url){
try{var u=new URL(url,window.location.href);
return u.origin===window.location.origin&&u.pathname.indexOf('/api/')===0;}
catch(e){return false;}
}
var orig=window.fetch?window.fetch.bind(window):null;
if(orig){
window.fetch=function(input,init){
try{
var url=(typeof input==='string')?input:(input&&input.url)||'';
if(isApi(url)){
var h=new Headers((init&&init.headers)||(typeof input!=='string'&&input&&input.headers)||{});
h.set('X-Devhub-Token',T);
init=Object.assign({},init,{headers:h});
}
}catch(e){}
return orig(input,init);
};
}
var XO=window.XMLHttpRequest&&window.XMLHttpRequest.prototype.open;
if(XO){
window.XMLHttpRequest.prototype.open=function(method,url){
var r=XO.apply(this,arguments);
try{if(isApi(url))this.setRequestHeader('X-Devhub-Token',T);}catch(e){}
return r;
};
}
})();`

// buildTokenScript returns the <script> tag that publishes the token and patches
// fetch/XHR for the given token.
func buildTokenScript(token string) []byte {
	tj, _ := json.Marshal(token)
	shim := strings.Replace(fetchShimJS, "%s", string(tj), 1)
	return []byte("<script>" + shim + "</script>")
}

// injectToken splices the token script immediately after the opening <head ...>
// tag (case-insensitive, attributes allowed); if there is no <head>, it prepends.
func injectToken(body, script []byte) []byte {
	lower := bytes.ToLower(body)
	if idx := bytes.Index(lower, []byte("<head")); idx != -1 {
		if rel := bytes.IndexByte(body[idx:], '>'); rel != -1 {
			pos := idx + rel + 1
			out := make([]byte, 0, len(body)+len(script))
			out = append(out, body[:pos]...)
			out = append(out, script...)
			out = append(out, body[pos:]...)
			return out
		}
	}
	out := make([]byte, 0, len(body)+len(script))
	out = append(out, script...)
	out = append(out, body...)
	return out
}
