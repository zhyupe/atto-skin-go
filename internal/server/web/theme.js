// Apply before the stylesheets to avoid a flash of the wrong theme.
(() => {
 const key='atto-theme';
 const system=window.matchMedia('(prefers-color-scheme: dark)');
 const valid=value=>value==='light'||value==='dark';
 let preference=null;
 try{const saved=localStorage.getItem(key);if(valid(saved))preference=saved}catch{}
 function apply(theme){
  document.documentElement.dataset.theme=theme;
  const button=document.getElementById('theme-toggle');
  if(button){button.textContent=theme==='light'?'浅色':'深色';button.setAttribute('aria-label',`切换至${theme==='light'?'深':'浅'}色模式`);button.title=`当前为${theme==='light'?'浅':'深'}色模式`;}
 }
 apply(preference||(system.matches?'dark':'light'));
 system.addEventListener('change',()=>{if(!preference)apply(system.matches?'dark':'light')});
 document.addEventListener('DOMContentLoaded',()=>{
  apply(document.documentElement.dataset.theme);
  document.getElementById('theme-toggle').addEventListener('click',()=>{
   preference=document.documentElement.dataset.theme==='light'?'dark':'light';apply(preference);
   try{localStorage.setItem(key,preference)}catch{}
  });
 });
})();
