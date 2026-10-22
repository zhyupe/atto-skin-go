const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const fixture=JSON.parse(fs.readFileSync(process.argv[2]));
const artifacts=process.env.ATTO_BROWSER_ARTIFACTS;
if(artifacts)fs.mkdirSync(artifacts,{recursive:true});
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.ATTO_CHROME?{executablePath:process.env.ATTO_CHROME}:{}),args:['--no-sandbox','--disable-dev-shm-usage']});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1040},colorScheme:'dark'});
  const page=await context.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
  const cookie={name:'atto_session',value:fixture.cookie,url:fixture.url,httpOnly:true,sameSite:'Lax'};
  async function screenshot(name){await page.waitForTimeout(200);if(artifacts)await page.screenshot({path:path.join(artifacts,name+'.png'),fullPage:true});}
  async function profile(){const response=await context.request.get(fixture.url+'/api/yggdrasil/sessionserver/session/minecraft/profile/'+fixture.uuid);assert.equal(response.status(),200);const p=await response.json();return JSON.parse(Buffer.from(p.properties.find(p=>p.name==='textures').value,'base64').toString()).textures.SKIN;}
  async function changeSkin(button,kind){await Promise.all([page.waitForEvent('framenavigated',{predicate:frame=>frame===page.mainFrame()}),button.click()]);await saved(kind);}
  async function openPanel(id){await page.locator(`[data-open="${id}"]`).first().click();await page.locator('#'+id+'[open]').waitFor();}
  async function closePanel(){await page.getByRole('button',{name:'关闭',exact:true}).click();}
  async function saved(kind){await page.waitForURL(u=>u.pathname==='/'&&u.searchParams.get('saved')===kind);await page.waitForFunction(()=>document.documentElement.dataset.enhanced==='true');}
  await page.goto(fixture.url);await page.getByRole('link',{name:'使用统一账号登录'}).waitFor();
  assert.equal(await page.locator('input[name="csrf"]').count(),0);await screenshot('login-dark');
  await context.addCookies([cookie]);await page.reload();await page.waitForFunction(()=>document.documentElement.dataset.enhanced==='true');
  assert.equal(await page.locator('.skin-item').count(),6);
  await page.waitForFunction(()=>{const c=document.querySelector('#character');return !c.hasAttribute('aria-busy')&&c.getContext('2d').getImageData(0,0,c.width,c.height).data.some((v,i)=>i%4===3&&v>0)});
  assert.equal(await page.locator('.stage-description').count(),0);
  assert.equal(await page.locator('.stage-state').textContent(),'当前皮肤');
  await screenshot('desktop-dark');
  await page.getByRole('button',{name:'切换至浅色模式'}).click();await screenshot('desktop-light');
  await page.reload();assert.equal(await page.locator('html').getAttribute('data-theme'),'light');
  for(const width of [320,390,768,1440]){await page.setViewportSize({width,height:1040});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`overflow ${width}`);}
  await page.setViewportSize({width:390,height:844});await screenshot('mobile-light');await page.getByRole('button',{name:'切换至深色模式'}).click();await screenshot('mobile-dark');
  await page.setViewportSize({width:1440,height:1040});
  const before=await profile();const form=page.locator('.skin-item:not(.selected)').first();const hash=await form.getAttribute('data-hash'),model=await form.getAttribute('data-model');
  await form.locator('button').click();await page.waitForFunction(()=>!document.querySelector('#character').hasAttribute('aria-busy'));
  assert.deepEqual(await profile(),before,'trial changed persisted skin');
  assert.equal(await page.locator('.stage').getAttribute('data-preview-state'),'unsaved');
  assert.equal(await page.locator('.stage-state').textContent(),'试穿中 · 未保存');
  assert.notEqual(await page.locator('.stage-state').evaluate(e=>getComputedStyle(e).backgroundColor),'rgba(0, 0, 0, 0)');
  await screenshot('trial-dark');
  await page.locator('#cancel-preview').click();assert.equal(await page.locator('.stage').getAttribute('data-preview-state'),'saved');
  assert.equal(await page.locator('.stage-state').textContent(),'当前皮肤');
  await form.locator('button').click();await page.waitForFunction(()=>!document.querySelector('#character').hasAttribute('aria-busy'));

  await page.getByRole('button',{name:'查看正面'}).click();const front=await page.locator('#character').evaluate(c=>c.toDataURL());await page.getByRole('button',{name:'查看背面'}).click();assert.notEqual(await page.locator('#character').evaluate(c=>c.toDataURL()),front);
  const name=await page.locator('#selected-name').textContent();await page.getByRole('button',{name:'切换至浅色模式'}).click();assert.equal(await page.locator('#selected-name').textContent(),name);
  await screenshot('trial-light');
  await page.setViewportSize({width:320,height:844});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await screenshot('trial-mobile-light');await page.setViewportSize({width:1440,height:1040});
  await changeSkin(page.locator('#apply'),'skin');assert.equal(await page.locator('.stage-state').textContent(),'当前皮肤');assert.equal((await profile()).url,fixture.url+'/textures/'+hash);assert.equal((await profile()).metadata.model,model);
  await openPanel('upload-panel');await page.getByLabel('皮肤文件').setInputFiles(path.join(fixture.dir,'legacy.png'));await page.getByLabel('手臂模型').selectOption('slim');await page.locator('#upload-submit').click();await page.getByRole('alert').filter({hasText:'64×32'}).waitFor();
  await page.getByLabel('皮肤文件').setInputFiles(path.join(fixture.dir,'upload.png'));await page.locator('#upload-submit').click();await page.locator('#upload-panel').waitFor({state:'hidden'});assert.equal(await page.locator('#selected-name').textContent(),'新皮肤');
  assert.equal((await profile()).url,fixture.url+'/textures/'+hash);
  await page.route('**/account/skin',route=>route.fulfill({status:429,contentType:'text/plain',body:'上传过于频繁，请稍后重试。'}));
  await page.locator('#apply').click();await page.locator('#notice').filter({hasText:'上传过于频繁'}).waitFor();assert.equal(await page.locator('#selected-name').textContent(),'新皮肤');
  assert.equal(await page.locator('#skin-file').evaluate(e=>e.files.length),1);
  assert.equal(await page.locator('.stage').getAttribute('data-preview-state'),'unsaved');
  await page.unroute('**/account/skin');await changeSkin(page.locator('#apply'),'skin');assert.notEqual((await profile()).url,fixture.url+'/textures/'+hash);assert.equal((await profile()).metadata.model,'slim');
  const count=await page.locator('.skin-item').count();assert.equal(count,7);
  await changeSkin(page.getByRole('button',{name:'恢复默认皮肤'}),'removed');assert.equal(await profile(),undefined);assert.equal(await page.locator('.skin-item').count(),count);
  await page.locator('.skin-select').first().click();await changeSkin(page.locator('#apply'),'skin');assert(await profile());
  // Unsupported/missing preview must not hide the real skin or disable switching.
  const failurePage=await context.newPage();await failurePage.route('**/textures/*',route=>route.fulfill({status:500,body:'unavailable'}));await failurePage.goto(fixture.url);await failurePage.locator('#preview-error').waitFor();assert(await failurePage.locator('#download').isVisible());await failurePage.close();
  // Server-rendered forms remain usable when JavaScript is disabled.
  const plain=await browser.newContext({javaScriptEnabled:false});await plain.addCookies([cookie]);const plainPage=await plain.newPage();await plainPage.goto(fixture.url);assert(await plainPage.locator('#upload-form').isVisible());assert(await plainPage.locator('#account-panel').isVisible());await plainPage.locator('.skin-select').last().click();await plainPage.waitForURL(u=>u.searchParams.get('saved')==='skin');await plain.close();
  await page.goto(fixture.url);await openPanel('account-panel');await page.getByLabel('Minecraft 角色名').fill('TakenName');await page.getByRole('button',{name:'保存名称'}).click();await page.waitForURL('**/account/profile');await page.locator('#account-panel .form-error').filter({hasText:'已被使用'}).waitFor();
  await page.getByLabel('Minecraft 角色名').fill('NewWardrobe');await page.getByRole('button',{name:'保存名称'}).click();await saved('profile');assert.equal(await page.locator('h1').textContent(),'NewWardrobe');
  await openPanel('connect-panel');assert.equal(await page.locator('#endpoint').inputValue(),fixture.url+'/api/yggdrasil/');await screenshot('connect-light');page.on('dialog',dialog=>dialog.accept());await page.getByRole('button',{name:'重新生成启动器密钥'}).click();await page.waitForURL('**/account/launcher-key');await page.locator('#connect-panel[open]').waitFor();const key=await page.locator('#launcher-key').inputValue();assert.match(key,/^[a-f0-9]{64}$/);
  const login=await context.request.post(fixture.url+'/api/yggdrasil/authserver/authenticate',{data:{username:'NewWardrobe',password:key,agent:{name:'Minecraft',version:1}}});assert.equal(login.status(),200);
  await page.goto(fixture.url);assert.equal(await page.locator('#launcher-key').count(),0);
  await openPanel('upload-panel');await screenshot('upload-light');await closePanel();
  await openPanel('account-panel');await page.getByRole('button',{name:'退出登录'}).click();await page.getByRole('link',{name:'使用统一账号登录'}).waitFor();assert.equal(await page.locator('.skin-item').count(),0);
  assert.deepEqual(errors,[]);console.log('Browser passed: both themes, responsive layouts, actual PNG rendering, trial/restore/upload/retry, legacy validation, default/history retention, no-JS forms, rename conflict, launcher key, logout and preview failure.');
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
