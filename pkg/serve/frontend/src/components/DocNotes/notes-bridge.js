// The notes bridge: the same picking the Markdown reader does, running INSIDE
// the sandboxed HTML document, and talking to the host by postMessage like
// ESCAPE_BRIDGE. The sandbox and the CSP are untouched; it is one more inline
// script under the policy that already allows them. The host only listens to
// its own iframe and validates every field it takes from here.
import { BLOCK_SELECTOR, INTERACTIVE_SELECTOR, QUESTION_LEAD, QUICK_REPLIES } from '../../data/doc-notes.js';

export const NOTES_PICK = 'moa-dn-pick';
export const NOTES_QUICK = 'moa-dn-quick';
export const NOTES_READY = 'moa-dn-ready';
export const NOTES_MARKS = 'moa-dn-marks';

export const NOTES_BRIDGE = String.raw`<script>(function(){
var SEL=${JSON.stringify(BLOCK_SELECTOR)},INTER=${JSON.stringify(INTERACTIVE_SELECTOR)},QRE=new RegExp(${JSON.stringify(QUESTION_LEAD.source)}),REPLIES=${JSON.stringify(QUICK_REPLIES)};
var st=document.createElement('style');
st.textContent='[data-dn]{cursor:pointer}.dn-picked{background:rgba(250,179,135,.22)!important;box-shadow:-3px 0 0 #fab387!important;border-radius:3px}.dn-noted{box-shadow:-3px 0 0 #fab387!important}.dn-sent{box-shadow:-3px 0 0 #9399b2!important}.dn-quick-row{display:flex;flex-wrap:wrap;gap:6px;margin:4px 0 14px}.dn-quick{min-height:32px;padding:0 12px;border:1px solid #7f849c;border-radius:16px;background:none;color:inherit;font:inherit;font-size:14px;cursor:pointer}.dn-quick.is-on{border-color:#fab387;background:rgba(250,179,135,.2)}';
(document.head||document.documentElement).appendChild(st);
function all(){return Array.prototype.slice.call(document.body.querySelectorAll(SEL));}
function clean(t){return t.replace(/\s+/g,' ').trim();}
function quote(el){
  if(el.tagName==='TR'){return Array.prototype.map.call(el.children,function(c){return clean(c.textContent);}).filter(Boolean).join(' | ');}
  var c=el.cloneNode(true);Array.prototype.forEach.call(c.querySelectorAll('ul,ol'),function(n){n.remove();});
  return clean(c.textContent);
}
function where(el){
  var h2='',h3='',hs=document.body.querySelectorAll('h1,h2,h3');
  for(var i=0;i<hs.length;i++){var h=hs[i];if(h===el||!(h.compareDocumentPosition(el)&4))break;
    if(h.tagName==='H2'){h2=clean(h.textContent);h3='';}else if(h.tagName==='H3')h3=clean(h.textContent);}
  return [h2,h3].filter(Boolean).join(' \u203a ');
}
function number(){
  all().forEach(function(el,i){el.setAttribute('data-dn',i);});
  Array.prototype.forEach.call(document.body.querySelectorAll('p[data-dn]'),function(p){
    var s=p.firstElementChild;
    if(!s||(s.tagName!=='STRONG'&&s.tagName!=='B')||!QRE.test(clean(s.textContent))||p.textContent.indexOf('?')<0)return;
    var n=p.nextElementSibling;if(n&&n.classList.contains('dn-quick-row'))return;
    var row=document.createElement('div');row.className='dn-quick-row';row.setAttribute('data-for',p.getAttribute('data-dn'));
    REPLIES.forEach(function(r){var b=document.createElement('button');b.type='button';b.className='dn-quick';b.textContent=r;b.setAttribute('data-reply',r);row.appendChild(b);});
    p.parentNode.insertBefore(row,p.nextSibling);
  });
}
number();
function send(type,el,extra){
  var m={type:type,index:Number(el.getAttribute('data-dn')),quote:quote(el),where:where(el)};
  for(var k in extra)m[k]=extra[k];
  parent.postMessage(m,'*');
}
document.addEventListener('click',function(e){
  var t=e.target;if(!t.closest)return;
  var q=t.closest('.dn-quick');
  if(q){var blk=document.querySelector('[data-dn="'+q.parentNode.getAttribute('data-for')+'"]');if(blk)send('${NOTES_QUICK}',blk,{reply:q.getAttribute('data-reply')});return;}
  if(t.closest(INTER))return;
  var s=getSelection();if(s&&!s.isCollapsed&&s.toString().trim())return;
  var b=t.closest('[data-dn]');if(b)send('${NOTES_PICK}',b,{precise:false});
});
var timer=null;
document.addEventListener('selectionchange',function(){
  clearTimeout(timer);
  timer=setTimeout(function(){
    var s=getSelection();if(!s||s.isCollapsed)return;
    var txt=clean(s.toString());if(txt.length<2)return;
    var n=s.anchorNode,el=n&&(n.nodeType===1?n:n.parentElement),b=el&&el.closest&&el.closest('[data-dn]');
    if(b){var m={type:'${NOTES_PICK}',index:Number(b.getAttribute('data-dn')),quote:txt,where:where(b),precise:true};parent.postMessage(m,'*');}
  },350);
});
window.addEventListener('message',function(e){
  if(e.source!==parent)return;var d=e.data||{};if(d.type!=='${NOTES_MARKS}')return;
  all().forEach(function(el){el.classList.remove('dn-picked','dn-noted','dn-sent');});
  Array.prototype.forEach.call(document.querySelectorAll('.dn-quick.is-on'),function(b){b.classList.remove('is-on');});
  function mark(i,c){var el=document.querySelector('[data-dn="'+i+'"]');if(el)el.classList.add(c);}
  (d.sent||[]).forEach(function(i){mark(i,'dn-sent');});
  (d.noted||[]).forEach(function(i){mark(i,'dn-noted');});
  if(typeof d.picked==='number')mark(d.picked,'dn-picked');
  (d.quick||[]).forEach(function(q){var b=document.querySelector('.dn-quick-row[data-for="'+q.index+'"] [data-reply="'+q.reply+'"]');if(b)b.classList.add('is-on');});
});
parent.postMessage({type:'${NOTES_READY}'},'*');
}());</script>`;
